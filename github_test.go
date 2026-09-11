package main

import (
	"slices"
	"testing"
)

func TestClassifyReviewStatus(t *testing.T) {
	human := func(login string) CommentEvent { return CommentEvent{Login: login} }
	bot := func(login string) CommentEvent { return CommentEvent{Login: login, IsBot: true} }

	multiTeam := []string{"matthyx", "alice"}

	tests := []struct {
		name   string
		events []CommentEvent
		team   []string
		want   string
	}{
		{
			name:   "empty conversation",
			events: nil,
			want:   "",
		},
		{
			name:   "only bots detected by IsBot",
			events: []CommentEvent{bot("some-ci"), bot("another-ci")},
			want:   "",
		},
		{
			// Proves the deny-list works independently of __typename.
			name:   "only bots detected by login deny-list",
			events: []CommentEvent{human("dependabot"), human("github-actions")},
			want:   "",
		},
		{
			// coderabbitai is the dominant bot on these repos. It is normally caught
			// by __typename; this pins the deny-list as an independent second line.
			name:   "coderabbitai after matthyx, caught by deny-list alone",
			events: []CommentEvent{human("matthyx"), human("coderabbitai")},
			want:   statusWaitingOnAuthor,
		},
		{
			name:   "matthyx spoke last",
			events: []CommentEvent{human("author"), human("matthyx")},
			want:   statusWaitingOnAuthor,
		},
		{
			name:   "matthyx earlier and a human replied",
			events: []CommentEvent{human("matthyx"), human("author")},
			want:   statusNeedsReviewer,
		},
		{
			name:   "matthyx earlier and only a bot spoke after",
			events: []CommentEvent{human("matthyx"), bot("coderabbitai")},
			want:   statusWaitingOnAuthor,
		},
		{
			name:   "matthyx never participated",
			events: []CommentEvent{human("author"), human("reviewer")},
			want:   "",
		},
		{
			name:   "matthyx is the only participant",
			events: []CommentEvent{human("matthyx")},
			want:   statusWaitingOnAuthor,
		},
		{
			name:   "multi-member team: alice spoke last",
			events: []CommentEvent{human("author"), human("alice")},
			team:   multiTeam,
			want:   statusWaitingOnAuthor,
		},
		{
			name:   "multi-member team: matthyx spoke first, alice spoke last",
			events: []CommentEvent{human("matthyx"), human("author"), human("alice")},
			team:   multiTeam,
			want:   statusWaitingOnAuthor,
		},
		{
			name:   "multi-member team: alice spoke earlier and author replied",
			events: []CommentEvent{human("alice"), human("author")},
			team:   multiTeam,
			want:   statusNeedsReviewer,
		},
		{
			name:   "multi-member team: neither matthyx nor alice participated",
			events: []CommentEvent{human("author"), human("reviewer")},
			team:   multiTeam,
			want:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			team := tc.team
			if team == nil {
				team = myTeam
			}
			if got := classifyReviewStatus(tc.events, team); got != tc.want {
				t.Errorf("classifyReviewStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveStatusName(t *testing.T) {
	tests := []struct {
		name   string
		values []singleSelectValue
		want   string
		// invariantHolds records whether the naive field-name-agnostic
		// "any option named To Archive" rule agrees with resolveStatusName.
		invariantHolds bool
	}{
		{
			name:           "a Status-named field wins",
			values:         []singleSelectValue{{FieldName: statusFieldName, OptionName: statusNeedsReviewer}},
			want:           statusNeedsReviewer,
			invariantHolds: true,
		},
		{
			name:           "differently-named field carrying a managed option name",
			values:         []singleSelectValue{{FieldName: "Workflow State", OptionName: statusToArchive}},
			want:           statusToArchive,
			invariantHolds: true,
		},
		{
			name:           "a Priority-only value resolves to nothing",
			values:         []singleSelectValue{{FieldName: "Priority", OptionName: "High"}},
			want:           "",
			invariantHolds: true,
		},
		{
			name:           "empty input",
			values:         nil,
			want:           "",
			invariantHolds: true,
		},
		{
			// The Status field says one thing while another field carries an option
			// literally named "To Archive". resolveStatusName correctly prefers the
			// Status field, so it disagrees with the naive rule that InArchive still
			// uses. Under fork B4 main.go's InArchive computation is never diffed, so
			// the two representations cannot be reconciled inside this change; this
			// case documents exactly where they part company.
			name: "Status field wins over another field carrying a managed option name",
			values: []singleSelectValue{
				{FieldName: statusFieldName, OptionName: statusNeedsReviewer},
				{FieldName: "Workflow State", OptionName: statusToArchive},
			},
			want:           statusNeedsReviewer,
			invariantHolds: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveStatusName(tc.values)
			if got != tc.want {
				t.Errorf("resolveStatusName() = %q, want %q", got, tc.want)
			}
			anyToArchive := slices.ContainsFunc(tc.values, func(v singleSelectValue) bool {
				return v.OptionName == statusToArchive
			})
			if holds := anyToArchive == (got == statusToArchive); holds != tc.invariantHolds {
				t.Errorf("InArchive/StatusName agreement = %v, want %v", holds, tc.invariantHolds)
			}
		})
	}
}
