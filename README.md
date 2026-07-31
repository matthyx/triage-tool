# triage-tool

Keeps the `kubescape` GitHub Projects boards in sync with reality: it adds untracked
open issues and pull requests to the bug and PR boards, moves closed or merged items
to **To Archive**, routes open PRs between **Waiting on Author** and **Needs Reviewer**
based on who spoke last in the conversation — plus whether the PR has a **merge conflict**
or an **outstanding change request** — and prints a priority report of the PRs needing
attention.

Routing only ever touches items whose current status is unset, *Waiting on Author*, or
*Needs Reviewer*, so a manually curated column is never overwritten. If the PR board is
not shaped as expected — a routing option missing, the options split across several
fields, or the status field not named `Status` — routing disables itself for that run
and prints the reason instead of guessing.

## Routing precedence

Every routing decision is made by one function, in this fixed order:

1. **Last commenter.** Work out the column the conversation implies: *Waiting on Author*
   if I spoke last, *Needs Reviewer* if I took part and someone else replied.
2. **Nothing to do.** If that rule has no opinion — I never spoke on the PR — the run
   stops here and the PR is left alone. No other signal can override this.
3. **Merge conflict.** Otherwise, if GitHub reports the PR as `CONFLICTING`, the column
   is *Waiting on Author*, reason `merge-conflict`.
4. **Outstanding change request.** Otherwise, if a change request is still unaddressed,
   the column is *Waiting on Author*, reason `changes-requested`.
5. **Fall through.** Otherwise the last-commenter answer stands, reason `last-commenter`.

Conflict outranks an outstanding change request only in the reason that gets logged —
both resolve to the same column. A merge conflict is a machine-verifiable fact while
"unaddressed change request" is a derived judgement, so the more objective reason is the
one printed.

Step 2 is what makes today's behaviour predictable: these signals can only **hold** a PR
in *Waiting on Author*; they never move a PR the last-commenter rule would have left
alone, and they never produce a *Needs Reviewer* the last-commenter rule did not already
produce. That is the current, deliberate behaviour rather than a permanent law — a future
change may need to relax it, and should re-measure its blast radius when it does.

### Merge conflicts

GitHub computes mergeability asynchronously, so `mergeable` is often `UNKNOWN` on a cold
query — around 30% of PRs in practice, collapsing to near zero on an immediate re-query.
`UNKNOWN` is treated as *no signal*, never as "not conflicting". A conflicted PR may
therefore keep its old column for a run or two; the next run picks it up.

### When a change request counts as "addressed"

A change request is considered addressed by **either** a commit newer than the review
**or** a comment from the PR author after it. Both halves are approximations, and they
are wrong in **both** directions:

- A rebase or cherry-pick can carry a commit timestamp older than the actual push, so a
  real fix can look unaddressed and the PR stays in *Waiting on Author*.
- The tip commit counts as an author action regardless of who actually pushed it, so a
  commit pushed by the **reviewer** reads as the author's fix. This is live: in
  `kubescape/node-agent#808` the tip commit was pushed by the reviewer, 30 days after the
  change request, and the PR reads as "addressed".

The second case is the reason this is safe to ship on approximations: **when the heuristic
guesses "addressed" wrongly, the tool simply behaves as it did before this feature
existed** — control falls through to the last-commenter answer, so no PR ends up anywhere
the old rule would not have put it.

The change request itself may come from **any** human reviewer, not just mine. Change
requests from bots are ignored. If GitHub says a change request is outstanding but none is
visible in the fetched conversation window, the PR is pinned conservatively.

## Report-only: stale approvals

Approved PRs are never routed, so a column set *before* the approval landed stays wrong
forever. Each one is reported:

```
approved pr <url> still in "Needs Reviewer" (routing does not manage approved PRs)
stale-approved: 6 approved pr(s) still in a managed column
```

**The tool does not correct these** — it only reports them. The scan mutates nothing and
runs even when routing has disabled itself on a misconfigured board, since that is exactly
when the information is most useful. Own PRs and drafts are exempt.

## Reading the routing output

| Line | Meaning |
| --- | --- |
| `moved pr <url> from "<a>" to "<b>" (<reason>)` | The board was changed. |
| `would move pr <url> from "<a>" to "<b>" (<reason>)` | Same, under `TRIAGE_ROUTING_DRY_RUN`. |
| `keeping pr <url> in "<status>" (<reason>)` | A new signal decided this PR's column and the PR is already there — **no mutation**. Printed only for `merge-conflict` and `changes-requested`; a plain `last-commenter` decision on a PR already in place stays silent. |
| `approved pr <url> still in "<status>"` | Stale approval, report only. |

The `by-conflict` and `by-changes-requested` counters in the routing summary count
**decisions, not mutations**, so they include PRs already sitting in the right column.
Each increment corresponds to exactly one printed line carrying that reason — a
`moved`/`would move` line, or a `keeping` line.

## Environment variables

| Variable | Purpose |
| --- | --- |
| `GITHUB_TOKEN` | **Required.** Token for the GraphQL API. Also read from a `.env` file. |
| `TRIAGE_ROUTING_DRY_RUN` | When set to any non-empty value, the routing step only prints the moves it *would* make. |

`TRIAGE_ROUTING_DRY_RUN` gates **the routing step only**. Adding items to boards and
archiving closed items still write to the live boards while it is set.

## Rollback

GitHub Projects V2 has no bulk undo, so recovery from a bad routing run is manual: set
`routableStatuses` to `[]string{}` to disable routing entirely, hand-correct the affected
items on the board, then re-review before re-enabling.

To disable only the **merge-conflict and change-request signals** while leaving
last-commenter routing intact, replace the body of `routeTarget` with a single line:

```go
return classifyReviewStatus(pr.Conversation, me), reasonLastCommenter
```

That is the kill switch for this feature. It restores the previous behaviour exactly,
since the last-commenter rule itself is never modified by the signals.
