package main

import (
	"slices"
	"strings"
	"time"
)

const (
	statusFieldName       = "Status"
	statusToArchive       = "To Archive"
	statusWaitingOnAuthor = "Waiting on Author"
	statusNeedsReviewer   = "Needs Reviewer"
	statusWIP             = "WIP"

	// mergeableConflicting is the one MergeableState that means the author must
	// act. MERGEABLE, UNKNOWN and "" are treated as absence of signal.
	mergeableConflicting = "CONFLICTING"
	mergeableMergeable   = "MERGEABLE"
	// reviewChangesRequested is both a Review.state and a reviewDecision value.
	reviewChangesRequested = "CHANGES_REQUESTED"
	// reviewApproved is a reviewDecision value.
	reviewApproved = "APPROVED"

	// reason* are the routing reasons routeTarget can return. They exist so the
	// printed line and the counter switch cannot drift apart by a typo.
	reasonMergeConflict    = "merge-conflict"
	reasonChangesRequested = "changes-requested"
	reasonLastCommenter    = "last-commenter"
	reasonOwnPR            = "own-pr"
)

// routableStatuses are the only current statuses the routing rule may overwrite.
// "To Archive" is deliberately absent: a human can park an open PR there to mean
// "stop showing me this", and routing must not yank it back out.
var routableStatuses = []string{"", statusWaitingOnAuthor, statusNeedsReviewer, statusWIP}

// CommentEvent is one conversation event, already stripped of GraphQL shapes.
type CommentEvent struct {
	Login string
	IsBot bool
	At    time.Time // comment createdAt / review submittedAt
	State string    // review state ("" for issue comments)
}

type PullRequestDetail struct {
	URL            string
	Title          string
	Repository     string
	Author         string
	IsDraft        bool
	UpdatedAt      time.Time
	ReviewDecision string
	CIState        string
	Conversation   []CommentEvent // oldest first
	Mergeable      string         // MergeableState: "MERGEABLE" | "CONFLICTING" | "UNKNOWN" | ""
	LastCommitAt   time.Time      // committedDate of the PR tip commit; zero when absent
}

// managedStatuses are the status names this tool knows how to set.
var managedStatuses = []string{statusToArchive, statusWaitingOnAuthor, statusNeedsReviewer, statusWIP}

// botLogins are actors whose comments never count as a human reply. This is
// secondary to the __typename check and only catches human-typed service accounts.
var botLogins = map[string]struct{}{
	"dependabot": {}, "github-actions": {}, "codecov": {},
	"renovate": {}, "sonarcloud": {}, "copilot-pull-request-reviewer": {},
	"coderabbitai": {},
}

func isBotLogin(login string) bool {
	if _, ok := botLogins[login]; ok {
		return true
	}
	return strings.HasSuffix(login, "[bot]")
}

// classifyReviewStatus returns the target column name for a PR given its
// chronological (oldest first) conversation, or "" when no move applies.
func classifyReviewStatus(events []CommentEvent, team []string) string {
	var humans []CommentEvent
	for _, e := range events {
		if e.IsBot || isBotLogin(e.Login) {
			continue
		}
		humans = append(humans, e)
	}
	if len(humans) == 0 {
		return ""
	}
	if slices.Contains(team, humans[len(humans)-1].Login) {
		return statusWaitingOnAuthor
	}
	// a team member participated and someone else spoke last, so the team was replied to.
	if slices.ContainsFunc(humans, func(e CommentEvent) bool { return slices.Contains(team, e.Login) }) {
		return statusNeedsReviewer
	}
	return ""
}

// newestChangeRequestAt returns the time of the newest non-bot CHANGES_REQUESTED
// review in the conversation, and whether one was found. It is generic over the
// reviewer: any human reviewer's change request counts, not just team members', so
// this function never references myTeam. It scans the whole slice rather than
// assuming the caller sorted it.
func newestChangeRequestAt(events []CommentEvent) (time.Time, bool) {
	var newest time.Time
	found := false
	for _, e := range events {
		if e.IsBot || isBotLogin(e.Login) {
			continue
		}
		if e.State != reviewChangesRequested {
			continue
		}
		if !found || e.At.After(newest) {
			newest = e.At
			found = true
		}
	}
	return newest, found
}

// changesRequestedOutstanding reports whether the PR carries a change request that
// the author has not yet addressed, per fork D3: addressed means either a commit
// newer than the newest change request, or an event from the PR author after it.
func changesRequestedOutstanding(pr PullRequestDetail) bool {
	// GitHub's aggregate is the gate; nothing else can turn the pin on.
	if pr.ReviewDecision != reviewChangesRequested {
		return false
	}
	at, ok := newestChangeRequestAt(pr.Conversation)
	if !ok {
		// Window miss: reviewDecision says CHANGES_REQUESTED but no human change
		// request is visible in the fetched window (full window / bot-only change
		// request / all-dismissed). Pin conservatively.
		return true
	}
	// D1: addressed by a push. The tip commit counts as an author action
	// regardless of who actually committed it - a known-false simplification
	// (kubescape/node-agent#808 has a tip commit by the reviewer). A false
	// "addressed" only costs the new protection: routeTarget's P4 then returns
	// classifyReviewStatus's own unmodified answer. A commit-authorship filter
	// is a deliberate follow-up, not a v2 clause.
	if !pr.LastCommitAt.IsZero() && pr.LastCommitAt.After(at) {
		return false
	}
	// D2: addressed by a reply from the PR author.
	for _, e := range pr.Conversation {
		if !e.IsBot && !isBotLogin(e.Login) && e.Login == pr.Author && e.At.After(at) {
			return false
		}
	}
	return true
}

// routeTarget decides the column an open PR belongs in and why, applying the
// signal precedence: own PR to WIP first, then last-commenter as a gate, then
// merge conflict, then an outstanding change request. Both overrides can only
// redirect a move toward "Waiting on Author"; neither can create a move the
// last-commenter rule did not already produce. Returns ("", "") when no move
// applies.
func routeTarget(pr PullRequestDetail, team []string) (string, string) {
	if slices.Contains(team, pr.Author) {
		return statusWIP, reasonOwnPR
	}
	// P0: the frozen last-commenter rule.
	target := classifyReviewStatus(pr.Conversation, team)
	// P1: MONOTONICITY GATE. No signal may create a routing candidate the
	// last-commenter rule did not already produce. Everything below this line
	// depends on it; weakening it to make a test pass is never the right fix.
	if target == "" {
		return "", ""
	}
	// P2: a merge conflict is a machine-verifiable fact, so it outranks P3 and
	// owns the printed reason when both hold. UNKNOWN and "" are no signal.
	if pr.Mergeable == mergeableConflicting {
		return statusWaitingOnAuthor, reasonMergeConflict
	}
	// P3: an outstanding change request the author has not addressed.
	if changesRequestedOutstanding(pr) {
		return statusWaitingOnAuthor, reasonChangesRequested
	}
	// P4: the structural safety net. Whenever an override fails to fire for any
	// reason, the answer is classifyReviewStatus's own, unchanged.
	return target, reasonLastCommenter
}
