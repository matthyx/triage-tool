# triage-tool

Keeps the `kubescape` GitHub Projects boards in sync with reality: it adds untracked
open issues and pull requests to the bug and PR boards, moves closed or merged items
to **To Archive**, routes open PRs between **Waiting on Author** and **Needs Reviewer**
based on who spoke last in the conversation, and prints a priority report of the PRs
needing attention.

Routing only ever touches items whose current status is unset, *Waiting on Author*, or
*Needs Reviewer*, so a manually curated column is never overwritten. If the PR board is
not shaped as expected — a routing option missing, the options split across several
fields, or the status field not named `Status` — routing disables itself for that run
and prints the reason instead of guessing.

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
