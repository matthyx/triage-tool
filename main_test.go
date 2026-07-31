package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	mapset "github.com/deckarep/golang-set/v2"
)

type MockGHClient struct {
	AddProjectItemFunc           func(ctx context.Context, owner, board, url string) error
	GetProjectIDFunc             func(ctx context.Context, owner, board string) (string, error)
	GetContentIDFunc             func(ctx context.Context, url string) (string, error)
	AddProjectItemWithIDsFunc    func(ctx context.Context, projectID, contentID string) error
	GetProjectItemsWithStateFunc func(ctx context.Context, owner, board string, limit int) ([]ProjectItem, error)
	GetBothProjectItemsFunc      func(ctx context.Context, owner, bugBoard, prBoard string, limit int) (mapset.Set[string], mapset.Set[string], error)
	GetIssuesAndPullsFunc        func(ctx context.Context, repo string, limit int) ([]string, []PullRequestDetail, error)
	GetRepositoriesFunc          func(ctx context.Context, owner string, limit int) ([]string, error)
	GetToArchiveFieldOptionFunc  func(ctx context.Context, owner, board string) (string, string, error)
	GetSingleSelectOptionsFunc   func(ctx context.Context, owner, board string) (map[string]FieldOption, error)
	UpdateProjectItemFieldFunc   func(ctx context.Context, projectID, itemID, fieldID, optionID string) error
}

func (m *MockGHClient) AddProjectItem(ctx context.Context, owner, board, url string) error {
	return m.AddProjectItemFunc(ctx, owner, board, url)
}

func (m *MockGHClient) GetProjectID(ctx context.Context, owner, board string) (string, error) {
	return m.GetProjectIDFunc(ctx, owner, board)
}

func (m *MockGHClient) GetContentID(ctx context.Context, url string) (string, error) {
	return m.GetContentIDFunc(ctx, url)
}

func (m *MockGHClient) AddProjectItemWithIDs(ctx context.Context, projectID, contentID string) error {
	return m.AddProjectItemWithIDsFunc(ctx, projectID, contentID)
}

func (m *MockGHClient) GetProjectItemsWithState(ctx context.Context, owner, board string, limit int) ([]ProjectItem, error) {
	return m.GetProjectItemsWithStateFunc(ctx, owner, board, limit)
}

func (m *MockGHClient) GetBothProjectItems(ctx context.Context, owner, bugBoard, prBoard string, limit int) (mapset.Set[string], mapset.Set[string], error) {
	return m.GetBothProjectItemsFunc(ctx, owner, bugBoard, prBoard, limit)
}

func (m *MockGHClient) GetIssuesAndPulls(ctx context.Context, repo string, limit int) ([]string, []PullRequestDetail, error) {
	return m.GetIssuesAndPullsFunc(ctx, repo, limit)
}

func (m *MockGHClient) GetRepositories(ctx context.Context, owner string, limit int) ([]string, error) {
	return m.GetRepositoriesFunc(ctx, owner, limit)
}

func (m *MockGHClient) GetToArchiveFieldOption(ctx context.Context, owner, board string) (string, string, error) {
	return m.GetToArchiveFieldOptionFunc(ctx, owner, board)
}

func (m *MockGHClient) GetSingleSelectOptions(ctx context.Context, owner, board string) (map[string]FieldOption, error) {
	return m.GetSingleSelectOptionsFunc(ctx, owner, board)
}

func (m *MockGHClient) UpdateProjectItemField(ctx context.Context, projectID, itemID, fieldID, optionID string) error {
	return m.UpdateProjectItemFieldFunc(ctx, projectID, itemID, fieldID, optionID)
}

func TestRun(t *testing.T) {
	ctx := t.Context()
	var mu sync.Mutex
	addedByProject := map[string]int{}

	mockClient := &MockGHClient{
		GetRepositoriesFunc: func(ctx context.Context, owner string, limit int) ([]string, error) {
			return []string{"kubescape/repo1"}, nil
		},
		GetIssuesAndPullsFunc: func(ctx context.Context, repo string, limit int) ([]string, []PullRequestDetail, error) {
			return []string{"https://github.com/kubescape/repo1/issues/1"}, []PullRequestDetail{
				{
					URL:            "https://github.com/kubescape/repo1/pull/1",
					Title:          "Stale PR (10 days)",
					Repository:     repo,
					IsDraft:        false,
					UpdatedAt:      time.Now().Add(-10 * 24 * time.Hour),
					ReviewDecision: "REVIEW_REQUIRED",
					CIState:        "SUCCESS",
				},
				{
					URL:            "https://github.com/kubescape/repo1/pull/2",
					Title:          "Stale PR (8 days)",
					Repository:     repo,
					IsDraft:        false,
					UpdatedAt:      time.Now().Add(-8 * 24 * time.Hour),
					ReviewDecision: "REVIEW_REQUIRED",
					CIState:        "SUCCESS",
				},
			}, nil
		},
		GetProjectItemsWithStateFunc: func(ctx context.Context, owner, board string, limit int) ([]ProjectItem, error) {
			return []ProjectItem{}, nil
		},
		GetProjectIDFunc: func(ctx context.Context, owner, board string) (string, error) {
			return "project-" + board, nil
		},
		GetContentIDFunc: func(ctx context.Context, url string) (string, error) {
			return "content-123", nil
		},
		AddProjectItemWithIDsFunc: func(ctx context.Context, projectID, contentID string) error {
			mu.Lock()
			defer mu.Unlock()
			addedByProject[projectID]++
			return nil
		},
		AddProjectItemFunc: func(ctx context.Context, owner, board, url string) error {
			return nil
		},
		GetToArchiveFieldOptionFunc: func(ctx context.Context, owner, board string) (string, string, error) {
			return "field-123", "option-123", nil
		},
		GetSingleSelectOptionsFunc: func(ctx context.Context, owner, board string) (map[string]FieldOption, error) {
			return statusOptions(), nil
		},
		UpdateProjectItemFieldFunc: func(ctx context.Context, projectID, itemID, fieldID, optionID string) error {
			return nil
		},
	}

	Run(ctx, mockClient)

	if addedByProject["project-"+bugTrackingBoard] != 1 {
		t.Errorf("expected 1 added issue, got %d", addedByProject["project-"+bugTrackingBoard])
	}
	if addedByProject["project-"+prTrackingBoard] != 2 {
		t.Errorf("expected 2 added pulls, got %d", addedByProject["project-"+prTrackingBoard])
	}
}

func TestClassifyReviewStatus(t *testing.T) {
	human := func(login string) CommentEvent { return CommentEvent{Login: login} }
	bot := func(login string) CommentEvent { return CommentEvent{Login: login, IsBot: true} }

	tests := []struct {
		name   string
		events []CommentEvent
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
			events: []CommentEvent{human(meLogin), human("coderabbitai")},
			want:   statusWaitingOnAuthor,
		},
		{
			name:   "matthyx spoke last",
			events: []CommentEvent{human("author"), human(meLogin)},
			want:   statusWaitingOnAuthor,
		},
		{
			name:   "matthyx earlier and a human replied",
			events: []CommentEvent{human(meLogin), human("author")},
			want:   statusNeedsReviewer,
		},
		{
			name:   "matthyx earlier and only a bot spoke after",
			events: []CommentEvent{human(meLogin), bot("coderabbitai")},
			want:   statusWaitingOnAuthor,
		},
		{
			name:   "matthyx never participated",
			events: []CommentEvent{human("author"), human("reviewer")},
			want:   "",
		},
		{
			name:   "matthyx is the only participant",
			events: []CommentEvent{human(meLogin)},
			want:   statusWaitingOnAuthor,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyReviewStatus(tc.events, meLogin); got != tc.want {
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

// statusOptions is the well-formed board shape: all three routing options on one
// single-select field literally named "Status", so Step 5a's preconditions pass.
func statusOptions() map[string]FieldOption {
	return map[string]FieldOption{
		statusToArchive:       {FieldID: "field-123", OptionID: "option-archive", FieldName: statusFieldName},
		statusWaitingOnAuthor: {FieldID: "field-123", OptionID: "option-waiting", FieldName: statusFieldName},
		statusNeedsReviewer:   {FieldID: "field-123", OptionID: "option-needs", FieldName: statusFieldName},
	}
}

type fieldUpdate struct {
	projectID string
	itemID    string
	fieldID   string
	optionID  string
}

func TestArchiveClosedItems(t *testing.T) {
	ctx := t.Context()
	var mu sync.Mutex
	var updates []fieldUpdate

	mockClient := &MockGHClient{
		GetRepositoriesFunc: func(ctx context.Context, owner string, limit int) ([]string, error) {
			return []string{"kubescape/repo1"}, nil
		},
		GetIssuesAndPullsFunc: func(ctx context.Context, repo string, limit int) ([]string, []PullRequestDetail, error) {
			return nil, nil, nil
		},
		GetProjectItemsWithStateFunc: func(ctx context.Context, owner, board string, limit int) ([]ProjectItem, error) {
			switch board {
			case bugTrackingBoard:
				return []ProjectItem{
					{ID: "bug-1", URL: "https://github.com/kubescape/repo1/issues/1", Closed: true, InArchive: false},
					{ID: "bug-2", URL: "https://github.com/kubescape/repo1/issues/2", Closed: true, InArchive: true},
					{ID: "bug-3", URL: "https://github.com/kubescape/repo1/issues/3", Closed: false, InArchive: false},
				}, nil
			case prTrackingBoard:
				return []ProjectItem{
					{ID: "pr-1", URL: "https://github.com/kubescape/repo1/pull/1", Closed: true, InArchive: false},
					{ID: "pr-2", URL: "https://github.com/kubescape/repo1/pull/2", Closed: true, InArchive: true},
					{ID: "pr-3", URL: "https://github.com/kubescape/repo1/pull/3", Closed: false, InArchive: false},
				}, nil
			}
			return []ProjectItem{}, nil
		},
		GetProjectIDFunc: func(ctx context.Context, owner, board string) (string, error) {
			return "project-" + board, nil
		},
		GetContentIDFunc: func(ctx context.Context, url string) (string, error) {
			return "content-123", nil
		},
		AddProjectItemWithIDsFunc: func(ctx context.Context, projectID, contentID string) error {
			return nil
		},
		AddProjectItemFunc: func(ctx context.Context, owner, board, url string) error {
			return nil
		},
		GetToArchiveFieldOptionFunc: func(ctx context.Context, owner, board string) (string, string, error) {
			return "field-123", "option-123", nil
		},
		GetSingleSelectOptionsFunc: func(ctx context.Context, owner, board string) (map[string]FieldOption, error) {
			return statusOptions(), nil
		},
		UpdateProjectItemFieldFunc: func(ctx context.Context, projectID, itemID, fieldID, optionID string) error {
			mu.Lock()
			defer mu.Unlock()
			updates = append(updates, fieldUpdate{projectID, itemID, fieldID, optionID})
			return nil
		},
	}

	Run(ctx, mockClient)

	want := []fieldUpdate{
		{"project-" + bugTrackingBoard, "bug-1", "field-123", "option-123"},
		{"project-" + prTrackingBoard, "pr-1", "field-123", "option-123"},
	}
	if len(updates) != len(want) {
		t.Fatalf("expected %d field updates, got %d: %+v", len(want), len(updates), updates)
	}
	for _, w := range want {
		if !slices.Contains(updates, w) {
			t.Errorf("missing expected field update %+v, got %+v", w, updates)
		}
	}
}

func prURL(n int) string {
	return fmt.Sprintf("https://github.com/kubescape/repo1/pull/%d", n)
}

// routingFixture is the shared board/PR fixture for the routing tests. The
// board-5 items and their PR details are chosen so that exactly one guard
// applies to each, making a missed guard visible as an extra mutation.
type routingFixture struct {
	mu             sync.Mutex
	updates        []fieldUpdate
	toArchiveCalls int
	optionsCalls   int

	options    map[string]FieldOption
	optionsErr error
}

func newRoutingFixture() *routingFixture {
	return &routingFixture{options: statusOptions()}
}

func (f *routingFixture) recorded() []fieldUpdate {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.updates)
}

// routingUpdates returns only the mutations issued by the routing block,
// distinguished from the archive block's by option ID.
func (f *routingFixture) routingUpdates() []fieldUpdate {
	var out []fieldUpdate
	for _, u := range f.recorded() {
		if u.optionID != "option-archive" {
			out = append(out, u)
		}
	}
	return out
}

func (f *routingFixture) client() *MockGHClient {
	conv := func(logins ...CommentEvent) []CommentEvent { return logins }
	human := func(login string) CommentEvent { return CommentEvent{Login: login} }
	bot := func(login string) CommentEvent { return CommentEvent{Login: login, IsBot: true} }

	prs := []PullRequestDetail{
		{URL: prURL(1), Author: "author", Conversation: conv(human("someone"), human(meLogin))},
		{URL: prURL(2), Author: "author", Conversation: conv(human(meLogin), human("author"))},
		{URL: prURL(3), Author: "author", Conversation: conv(human(meLogin), human("author"))},
		{URL: prURL(4), Author: "author", Conversation: conv(human("someone"), human(meLogin))},
		{URL: prURL(5), Author: "author", Conversation: conv(human("someone"), human(meLogin))},
		{URL: prURL(6), Author: "author", Conversation: conv(human("someone"), human(meLogin))},
		{URL: prURL(7), Author: "author", ReviewDecision: "APPROVED", Conversation: conv(human("someone"), human(meLogin))},
		{URL: prURL(8), Author: "author", IsDraft: true, Conversation: conv(human("someone"), human(meLogin))},
		{URL: prURL(9), Author: meLogin, Conversation: conv(human("someone"), human(meLogin))},
		{URL: prURL(10), Author: "author", Conversation: conv(human(meLogin), bot("dependabot"))},
	}

	items := []ProjectItem{
		{ID: "pr-1", URL: prURL(1)},
		{ID: "pr-2", URL: prURL(2)},
		{ID: "pr-3", URL: prURL(3), StatusName: statusNeedsReviewer},
		{ID: "pr-4", URL: prURL(4), Closed: true},
		{ID: "pr-5", URL: prURL(5), Closed: true, InArchive: true, StatusName: statusToArchive},
		{ID: "pr-6", URL: prURL(6), StatusName: "In Progress"},
		{ID: "pr-7", URL: prURL(7)},
		{ID: "pr-8", URL: prURL(8)},
		{ID: "pr-9", URL: prURL(9)},
		{ID: "pr-10", URL: prURL(10)},
	}

	return &MockGHClient{
		GetRepositoriesFunc: func(ctx context.Context, owner string, limit int) ([]string, error) {
			return []string{"kubescape/repo1"}, nil
		},
		GetIssuesAndPullsFunc: func(ctx context.Context, repo string, limit int) ([]string, []PullRequestDetail, error) {
			return nil, prs, nil
		},
		GetProjectItemsWithStateFunc: func(ctx context.Context, owner, board string, limit int) ([]ProjectItem, error) {
			if board == prTrackingBoard {
				return items, nil
			}
			return []ProjectItem{}, nil
		},
		GetProjectIDFunc: func(ctx context.Context, owner, board string) (string, error) {
			return "project-" + board, nil
		},
		GetContentIDFunc: func(ctx context.Context, url string) (string, error) {
			return "content-123", nil
		},
		AddProjectItemWithIDsFunc: func(ctx context.Context, projectID, contentID string) error {
			return nil
		},
		AddProjectItemFunc: func(ctx context.Context, owner, board, url string) error {
			return nil
		},
		GetToArchiveFieldOptionFunc: func(ctx context.Context, owner, board string) (string, string, error) {
			f.mu.Lock()
			f.toArchiveCalls++
			f.mu.Unlock()
			return "field-123", "option-archive", nil
		},
		GetSingleSelectOptionsFunc: func(ctx context.Context, owner, board string) (map[string]FieldOption, error) {
			f.mu.Lock()
			f.optionsCalls++
			f.mu.Unlock()
			if f.optionsErr != nil {
				return nil, f.optionsErr
			}
			return f.options, nil
		},
		UpdateProjectItemFieldFunc: func(ctx context.Context, projectID, itemID, fieldID, optionID string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.updates = append(f.updates, fieldUpdate{projectID, itemID, fieldID, optionID})
			return nil
		},
	}
}

// captureOutput redirects os.Stdout for the duration of fn. Safe under -race:
// fn joins every goroutine it starts before returning.
func captureOutput(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	out := <-done
	_ = r.Close()
	return out
}

func TestRunRoutesPRsByLastCommenter(t *testing.T) {
	ctx := t.Context()
	f := newRoutingFixture()

	out := captureOutput(t, func() { Run(ctx, f.client()) })

	want := []fieldUpdate{
		{"project-" + prTrackingBoard, "pr-1", "field-123", "option-waiting"},
		{"project-" + prTrackingBoard, "pr-2", "field-123", "option-needs"},
		{"project-" + prTrackingBoard, "pr-4", "field-123", "option-archive"},
		{"project-" + prTrackingBoard, "pr-10", "field-123", "option-waiting"},
	}
	got := f.recorded()
	if len(got) != len(want) {
		t.Fatalf("expected %d mutations, got %d: %+v", len(want), len(got), got)
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("missing expected mutation %+v, got %+v", w, got)
		}
	}

	if f.toArchiveCalls != 2 {
		t.Errorf("expected 2 GetToArchiveFieldOption call sites, got %d", f.toArchiveCalls)
	}
	if f.optionsCalls != 1 {
		t.Errorf("expected 1 GetSingleSelectOptions call site, got %d", f.optionsCalls)
	}
	if !strings.Contains(out, "routing: ") {
		t.Errorf("expected a routing summary line, got:\n%s", out)
	}
	// pr-6 sits in an unmanaged status and must be counted, not moved.
	if !strings.Contains(out, "1 skipped-unmanaged-status") {
		t.Errorf("expected 1 skipped-unmanaged-status in summary, got:\n%s", out)
	}
	// pr-3 is already in its target column.
	if !strings.Contains(out, "1 already-in-target") {
		t.Errorf("expected 1 already-in-target in summary, got:\n%s", out)
	}
}

func TestRunRoutingDryRun(t *testing.T) {
	t.Setenv("TRIAGE_ROUTING_DRY_RUN", "1")
	ctx := t.Context()
	f := newRoutingFixture()

	out := captureOutput(t, func() { Run(ctx, f.client()) })

	if routed := f.routingUpdates(); len(routed) != 0 {
		t.Errorf("dry run must issue no routing mutations, got %+v", routed)
	}
	// The flag deliberately scopes to routing only: archiving still writes.
	want := fieldUpdate{"project-" + prTrackingBoard, "pr-4", "field-123", "option-archive"}
	if got := f.recorded(); !slices.Contains(got, want) {
		t.Errorf("archive mutation for pr-4 must still fire under dry run, got %+v", got)
	}
	if !strings.Contains(out, `would move pr `+prURL(1)+` from "(none)" to "Waiting on Author"`) {
		t.Errorf("expected a would-move line naming the replaced status, got:\n%s", out)
	}
}

func TestRunRoutingDisabledOnMisconfiguredBoard(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(f *routingFixture)
		wantLog string
	}{
		{
			name: "missing Needs Reviewer option",
			mutate: func(f *routingFixture) {
				delete(f.options, statusNeedsReviewer)
			},
			wantLog: "routing disabled: board is missing",
		},
		{
			name: "routing options span multiple fields",
			mutate: func(f *routingFixture) {
				o := f.options[statusWaitingOnAuthor]
				o.FieldID = "field-999"
				f.options[statusWaitingOnAuthor] = o
			},
			wantLog: "routing options span multiple fields, disabling routing",
		},
		{
			name: "status field is not named Status",
			mutate: func(f *routingFixture) {
				for name, o := range f.options {
					o.FieldName = "Workflow State"
					f.options[name] = o
				}
			},
			wantLog: `status field is not named "Status", disabling routing`,
		},
		{
			name: "options query fails",
			mutate: func(f *routingFixture) {
				f.optionsErr = errors.New("boom")
			},
			wantLog: "routing disabled: boom",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newRoutingFixture()
			tc.mutate(f)

			out := captureOutput(t, func() { Run(t.Context(), f.client()) })

			if routed := f.routingUpdates(); len(routed) != 0 {
				t.Errorf("expected zero routing mutations, got %+v", routed)
			}
			if !strings.Contains(out, tc.wantLog) {
				t.Errorf("expected log %q, got:\n%s", tc.wantLog, out)
			}
		})
	}
}
