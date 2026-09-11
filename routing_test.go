package main

import (
	"fmt"
	"testing"
	"time"
)

func TestChangesRequestedOutstanding(t *testing.T) {
	base := signalBase
	tests := []struct {
		name string
		pr   PullRequestDetail
		want bool
	}{
		{
			name: "review decision is not CHANGES_REQUESTED",
			pr: PullRequestDetail{Author: "author", ReviewDecision: "APPROVED",
				Conversation: []CommentEvent{cr(base, "matthyx")}},
			want: false,
		},
		{
			name: "change request with nothing after it",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{cr(base, "matthyx")}},
			want: true,
		},
		{
			name: "addressed by a later commit",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{cr(base, "matthyx")}, LastCommitAt: base.Add(time.Hour)},
			want: false,
		},
		{
			name: "addressed by a later author comment",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{cr(base, "matthyx"), at(base.Add(time.Hour), "author")}},
			want: false,
		},
		{
			// The reported G2 gap: a third party speaking last must not read as
			// the author having addressed anything.
			name: "later comment by a non-author human",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{cr(base, "matthyx"), at(base.Add(time.Hour), "thirdparty")}},
			want: true,
		},
		{
			name: "later comment by a bot",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{cr(base, "matthyx"), botAt(base.Add(time.Hour), "coderabbitai")}},
			want: true,
		},
		{
			name: "author comment predates the change request",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{at(base.Add(-time.Hour), "author"), cr(base, "matthyx")}},
			want: true,
		},
		{
			// Only the older of two change requests is addressed, so the newer
			// one still stands. Also proves the newest is the one compared.
			name: "two change requests, only the older is addressed",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{
					cr(base.Add(-2*time.Hour), "matthyx"),
					at(base.Add(-time.Hour), "author"),
					cr(base, "matthyx"),
				}},
			want: true,
		},
		{
			// Path (i): the change request fell outside the fetched window.
			name: "change request absent from the conversation window",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{at(base, "thirdparty")}},
			want: true,
		},
		{
			// Path (ii): the only change request came from a bot reviewer.
			name: "bot-only change request, review decision still CHANGES_REQUESTED",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{{Login: "coderabbitai", IsBot: true, At: base, State: reviewChangesRequested}}},
			want: true,
		},
		{
			name: "zero LastCommitAt falls through to the author-reply check",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{cr(base, "matthyx")}, LastCommitAt: time.Time{}},
			want: true,
		},
		{
			// Reviewer-generic: the change request is dakshhhhh16's, not
			// matthyx's, and must fire identically. Exhibit kubescape#2608.
			name: "change request by a non-matthyx human reviewer",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{cr(base, "dakshhhhh16"), at(base.Add(time.Hour), "thirdparty")}},
			want: true,
		},
		{
			// A18, pinned as documented current behaviour: the tip commit counts
			// as an author action even when the committer is someone else - live
			// exhibit kubescape/node-agent#808, where the reviewer pushed. A
			// future commit-authorship filter must consciously flip this row.
			name: "later commit whose committer is not the PR author still reads as addressed",
			pr: PullRequestDetail{Author: "entlein", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{cr(base, "matthyx")}, LastCommitAt: base.Add(720 * time.Hour)},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := changesRequestedOutstanding(tc.pr); got != tc.want {
				t.Errorf("changesRequestedOutstanding() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRouteTarget(t *testing.T) {
	base := signalBase
	// needsReviewerConv classifies as "Needs Reviewer": me spoke, someone else
	// spoke last. waitingConv classifies as "Waiting on Author": me spoke last.
	needsReviewerConv := []CommentEvent{at(base.Add(-2*time.Hour), "matthyx"), at(base.Add(-time.Hour), "author")}
	waitingConv := []CommentEvent{at(base.Add(-2*time.Hour), "author"), at(base.Add(-time.Hour), "matthyx")}
	// noneConv never mentions me, so classifyReviewStatus returns "".
	noneConv := []CommentEvent{at(base.Add(-2*time.Hour), "author"), at(base.Add(-time.Hour), "thirdparty")}
	// crConv classifies as "Needs Reviewer" AND carries an outstanding change
	// request: me spoke, submitted the change request at base, and a third party
	// (not the PR author, so D2 cannot fire) spoke last. The change request must
	// not be the final event, or me would be the last speaker and the classifier
	// would say "Waiting on Author" instead.
	crConv := []CommentEvent{
		at(base.Add(-2*time.Hour), "matthyx"),
		cr(base, "matthyx"),
		at(base.Add(time.Hour), "thirdparty"),
	}
	// crNoneConv carries the same outstanding change request but from a reviewer
	// who is not me, and me never appears - so the classifier returns "" and P1
	// must gate before the change-request override can fire.
	crNoneConv := []CommentEvent{
		at(base.Add(-2*time.Hour), "author"),
		cr(base, "reviewer"),
		at(base.Add(time.Hour), "thirdparty"),
	}

	tests := []struct {
		name       string
		pr         PullRequestDetail
		team       []string
		wantTarget string
		wantReason string
	}{
		{
			name:       "no classifier verdict plus conflict stays inert",
			pr:         PullRequestDetail{Author: "author", Conversation: noneConv, Mergeable: mergeableConflicting},
			wantTarget: "", wantReason: "",
		},
		{
			name: "no classifier verdict plus outstanding change request stays inert",
			pr: PullRequestDetail{Author: "author", Conversation: crNoneConv,
				ReviewDecision: reviewChangesRequested},
			wantTarget: "", wantReason: "",
		},
		{
			name:       "needs reviewer overridden by merge conflict",
			pr:         PullRequestDetail{Author: "author", Conversation: needsReviewerConv, Mergeable: mergeableConflicting},
			wantTarget: statusWaitingOnAuthor, wantReason: reasonMergeConflict,
		},
		{
			name: "needs reviewer overridden by outstanding change request",
			pr: PullRequestDetail{Author: "author", Conversation: crConv,
				ReviewDecision: reviewChangesRequested},
			wantTarget: statusWaitingOnAuthor, wantReason: reasonChangesRequested,
		},
		{
			name: "conflict outranks change request",
			pr: PullRequestDetail{Author: "author", Conversation: crConv,
				ReviewDecision: reviewChangesRequested, Mergeable: mergeableConflicting},
			wantTarget: statusWaitingOnAuthor, wantReason: reasonMergeConflict,
		},
		{
			name:       "unknown mergeable is no signal",
			pr:         PullRequestDetail{Author: "author", Conversation: needsReviewerConv, Mergeable: "UNKNOWN"},
			wantTarget: statusNeedsReviewer, wantReason: reasonLastCommenter,
		},
		{
			name:       "empty mergeable is no signal",
			pr:         PullRequestDetail{Author: "author", Conversation: needsReviewerConv},
			wantTarget: statusNeedsReviewer, wantReason: reasonLastCommenter,
		},
		{
			name: "change request addressed by a commit",
			pr: PullRequestDetail{Author: "author", Conversation: crConv,
				ReviewDecision: reviewChangesRequested, LastCommitAt: base.Add(2 * time.Hour)},
			wantTarget: statusNeedsReviewer, wantReason: reasonLastCommenter,
		},
		{
			// Same column as the classifier would give, different reason: the
			// reason is load-bearing information, not decoration.
			name:       "waiting on author with a merge conflict reports the conflict",
			pr:         PullRequestDetail{Author: "author", Conversation: waitingConv, Mergeable: mergeableConflicting},
			wantTarget: statusWaitingOnAuthor, wantReason: reasonMergeConflict,
		},
		{
			name:       "waiting on author with no signals",
			pr:         PullRequestDetail{Author: "author", Conversation: waitingConv},
			wantTarget: statusWaitingOnAuthor, wantReason: reasonLastCommenter,
		},
		{
			name: "own PR is unconditionally routed to WIP",
			pr: PullRequestDetail{Author: "matthyx", Conversation: needsReviewerConv,
				ReviewDecision: reviewChangesRequested, Mergeable: mergeableConflicting},
			wantTarget: statusWIP, wantReason: reasonOwnPR,
		},
		{
			name:       "own PR with no conversation is unconditionally routed to WIP",
			pr:         PullRequestDetail{Author: "matthyx"},
			wantTarget: statusWIP, wantReason: reasonOwnPR,
		},
		{
			name:       "own PR by another team member is unconditionally routed to WIP",
			pr:         PullRequestDetail{Author: "alice", Conversation: needsReviewerConv},
			team:       []string{"matthyx", "alice"},
			wantTarget: statusWIP, wantReason: reasonOwnPR,
		},
		{
			name:       "draft PR is routed by last-commenter",
			pr:         PullRequestDetail{Author: "author", IsDraft: true, Conversation: waitingConv},
			wantTarget: statusWaitingOnAuthor, wantReason: reasonLastCommenter,
		},
		{
			name:       "approved PR is routed by last-commenter",
			pr:         PullRequestDetail{Author: "author", ReviewDecision: reviewApproved, Conversation: needsReviewerConv},
			wantTarget: statusNeedsReviewer, wantReason: reasonLastCommenter,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			team := tc.team
			if team == nil {
				team = myTeam
			}
			target, reason := routeTarget(tc.pr, team)
			if target != tc.wantTarget || reason != tc.wantReason {
				t.Errorf("routeTarget() = (%q, %q), want (%q, %q)", target, reason, tc.wantTarget, tc.wantReason)
			}
		})
	}
}

// TestRouteTargetMonotonicity walks the full 6x4x4x3 = 288-case cross-product of
// conversation shape, ReviewDecision, Mergeable and LastCommitAt, and asserts
// that routeTarget can only ever withhold a move or redirect one toward
// "Waiting on Author".
//
// Read a green run correctly: the property is TRUE BY CONSTRUCTION today. P1
// short-circuits on an empty classifier verdict before any override can fire, so
// passing proves the code matches the design - it does not prove the design was
// discovered to be correct, and it is not evidence that monotonicity is a subtle
// emergent fact. The test's real value is as a regression guard against future
// edits to routeTarget: the moment someone adds a signal above P1, or an
// override that can return "Needs Reviewer", this fails loudly instead of the
// change reaching the board.
func TestRouteTargetMonotonicity(t *testing.T) {
	base := signalBase
	before := base.Add(-time.Hour)
	after := base.Add(time.Hour)

	// Every shape is built off the same base so before/after is exact. Where a
	// CHANGES_REQUESTED review is present it sits exactly at base.
	conversations := map[string][]CommentEvent{
		"empty":                 {},
		"bot-only":              {botAt(before, "coderabbitai"), botAt(after, "dependabot")},
		"me-last":               {at(before, "author"), cr(base, "matthyx")},
		"other-last-me-present": {at(before, "matthyx"), cr(base, "reviewer"), at(after, "thirdparty")},
		"other-last-me-absent":  {cr(base, "reviewer"), at(after, "thirdparty")},
		"author-last":           {at(before, "matthyx"), cr(base, "reviewer"), at(after, "author")},
	}
	convNames := []string{"empty", "bot-only", "me-last", "other-last-me-present", "other-last-me-absent", "author-last"}
	decisions := []string{"", "APPROVED", "CHANGES_REQUESTED", "REVIEW_REQUIRED"}
	mergeables := []string{"", "MERGEABLE", "CONFLICTING", "UNKNOWN"}
	commitTimes := map[string]time.Time{"zero": {}, "before-CR": before, "after-CR": after}
	commitNames := []string{"zero", "before-CR", "after-CR"}

	cases := 0
	for _, cn := range convNames {
		for _, decision := range decisions {
			for _, mergeable := range mergeables {
				for _, tn := range commitNames {
					cases++
					pr := PullRequestDetail{
						Author:         "author",
						Conversation:   conversations[cn],
						ReviewDecision: decision,
						Mergeable:      mergeable,
						LastCommitAt:   commitTimes[tn],
					}
					got, reason := routeTarget(pr, myTeam)
					want := classifyReviewStatus(pr.Conversation, myTeam)
					desc := fmt.Sprintf("conv=%s decision=%q mergeable=%q commit=%s", cn, decision, mergeable, tn)

					// 2. No candidate is created.
					if got != "" && want == "" {
						t.Errorf("%s: routeTarget created a candidate %q the classifier did not", desc, got)
					}
					// 3. No new "Needs Reviewer".
					if got == statusNeedsReviewer && want != statusNeedsReviewer {
						t.Errorf("%s: routeTarget produced %q but the classifier said %q", desc, got, want)
					}
					// 4. The only permitted redirection is toward "Waiting on Author".
					if got != "" && got != want && got != statusWaitingOnAuthor {
						t.Errorf("%s: routeTarget redirected %q -> %q, which is not toward %q", desc, want, got, statusWaitingOnAuthor)
					}
					// 5. The reason is one of the three consts, and is "" exactly
					// when the target is "".
					switch reason {
					case reasonMergeConflict, reasonChangesRequested, reasonLastCommenter:
						if got == "" {
							t.Errorf("%s: empty target carried reason %q", desc, reason)
						}
					case "":
						if got != "" {
							t.Errorf("%s: target %q carried an empty reason", desc, got)
						}
					default:
						t.Errorf("%s: unknown reason %q", desc, reason)
					}
				}
			}
		}
	}
	if cases != 288 {
		t.Errorf("expected 288 enumerated cases, walked %d", cases)
	}
}
