# triage-tool

Keeps the `kubescape` GitHub Projects boards in sync with reality: it adds untracked
open issues and pull requests to the bug and PR boards, moves closed or merged items
to **To Archive**, moves team PRs (`myTeam`) to **WIP**, routes open PRs between **Waiting on Author**
and **Needs Reviewer** based on who spoke last in the conversation — plus whether the PR has a
**merge conflict** or an **outstanding change request** — and prints a priority report of the PRs
needing attention.

Routing only ever touches items whose current status is unset, *Waiting on Author*,
*Needs Reviewer*, or *WIP*, so a manually curated column is never overwritten. If the PR board is
not shaped as expected — a routing option missing, the options split across several
fields, or the status field not named `Status` — routing disables itself for that run
and prints the reason instead of guessing.

## Routing precedence

Every routing decision is made by one function, in this fixed order:

1. **Own / Team PR.** If the PR is authored by a member of `myTeam`, the column is *WIP*, reason `own-pr`.
2. **Last commenter.** Work out the column the conversation implies: *Waiting on Author*
   if someone on the team spoke last, *Needs Reviewer* if the team took part and someone else replied.
3. **Nothing to do.** If that rule has no opinion — the team never spoke on the PR — the run
   stops here and the PR is left alone. No other signal can override this.
4. **Merge conflict.** Otherwise, if GitHub reports the PR as `CONFLICTING`, the column
   is *Waiting on Author*, reason `merge-conflict`.
5. **Outstanding change request.** Otherwise, if a change request is still unaddressed,
   the column is *Waiting on Author*, reason `changes-requested`.
6. **Fall through.** Otherwise the last-commenter answer stands, reason `last-commenter`.

Conflict outranks an outstanding change request only in the reason that gets logged —
both resolve to the same column. A merge conflict is a machine-verifiable fact while
"unaddressed change request" is a derived judgement, so the more objective reason is the
one printed.

Step 3 is what makes non-own PR behaviour predictable: these signals can only **hold** a PR
in *Waiting on Author*; they never move a PR the last-commenter rule would have left
alone, and they never produce a *Needs Reviewer* the last-commenter rule did not already
produce.

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

## Reading the routing output

| Line | Meaning |
| --- | --- |
| `moved pr <url> from "<a>" to "<b>" (<reason>)` | The board was changed. |
| `would move pr <url> from "<a>" to "<b>" (<reason>)` | Same, under `TRIAGE_ROUTING_DRY_RUN`. |
| `keeping pr <url> in "<status>" (<reason>)` | A signal decided this PR's column and the PR is already there — **no mutation**. Printed for non-`last-commenter` reasons (`merge-conflict`, `changes-requested`, `own-pr`); a plain `last-commenter` decision on a PR already in place stays silent. |

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
return classifyReviewStatus(pr.Conversation, team), reasonLastCommenter
```

That is the kill switch for this feature. It restores the previous behaviour exactly,
since the last-commenter rule itself is never modified by the signals.
