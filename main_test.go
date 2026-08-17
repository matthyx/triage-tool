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

func init() {
	createMulticaIssue = func(ctx context.Context, repoName, prNumber, fullURL string) error {
		return nil
	}
}


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

// statusOptions is the well-formed board shape: all four routing options on one
// single-select field literally named "Status", so Step 5a's preconditions pass.
func statusOptions() map[string]FieldOption {
	return map[string]FieldOption{
		statusToArchive:       {FieldID: "field-123", OptionID: "option-archive", FieldName: statusFieldName},
		statusWaitingOnAuthor: {FieldID: "field-123", OptionID: "option-waiting", FieldName: statusFieldName},
		statusNeedsReviewer:   {FieldID: "field-123", OptionID: "option-needs", FieldName: statusFieldName},
		statusWIP:             {FieldID: "field-123", OptionID: "option-wip", FieldName: statusFieldName},
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

// at builds a timestamped human conversation event. The client()-local human()
// and bot() closures are byte-identical-protected, so timestamped fixtures get
// their own package-level helpers rather than extending those.
func at(t time.Time, login string) CommentEvent { return CommentEvent{Login: login, At: t} }

// cr builds a timestamped human CHANGES_REQUESTED review event.
func cr(t time.Time, login string) CommentEvent {
	return CommentEvent{Login: login, At: t, State: reviewChangesRequested}
}

// botAt builds a timestamped bot conversation event.
func botAt(t time.Time, login string) CommentEvent {
	return CommentEvent{Login: login, IsBot: true, At: t}
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

	// extraPRs/extraItems are appended to the ten base rows so new cases can be
	// added without touching them. Empty by default, so every pre-existing test
	// behaves exactly as before.
	extraPRs   []PullRequestDetail
	extraItems []ProjectItem
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

	prs = append(prs, f.extraPRs...)
	items = append(items, f.extraItems...)

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
		{"project-" + prTrackingBoard, "pr-7", "field-123", "option-waiting"},
		{"project-" + prTrackingBoard, "pr-8", "field-123", "option-waiting"},
		{"project-" + prTrackingBoard, "pr-9", "field-123", "option-wip"},
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
			name: "missing WIP option",
			mutate: func(f *routingFixture) {
				delete(f.options, statusWIP)
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

// signalBase is the fixed reference time the signal fixtures hang off, so the
// before/after relationships in them are exact rather than wall-clock dependent.
var signalBase = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

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
				Conversation: []CommentEvent{cr(base, meLogin)}},
			want: false,
		},
		{
			name: "change request with nothing after it",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{cr(base, meLogin)}},
			want: true,
		},
		{
			name: "addressed by a later commit",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{cr(base, meLogin)}, LastCommitAt: base.Add(time.Hour)},
			want: false,
		},
		{
			name: "addressed by a later author comment",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{cr(base, meLogin), at(base.Add(time.Hour), "author")}},
			want: false,
		},
		{
			// The reported G2 gap: a third party speaking last must not read as
			// the author having addressed anything.
			name: "later comment by a non-author human",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{cr(base, meLogin), at(base.Add(time.Hour), "thirdparty")}},
			want: true,
		},
		{
			name: "later comment by a bot",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{cr(base, meLogin), botAt(base.Add(time.Hour), "coderabbitai")}},
			want: true,
		},
		{
			name: "author comment predates the change request",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{at(base.Add(-time.Hour), "author"), cr(base, meLogin)}},
			want: true,
		},
		{
			// Only the older of two change requests is addressed, so the newer
			// one still stands. Also proves the newest is the one compared.
			name: "two change requests, only the older is addressed",
			pr: PullRequestDetail{Author: "author", ReviewDecision: reviewChangesRequested,
				Conversation: []CommentEvent{
					cr(base.Add(-2*time.Hour), meLogin),
					at(base.Add(-time.Hour), "author"),
					cr(base, meLogin),
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
				Conversation: []CommentEvent{cr(base, meLogin)}, LastCommitAt: time.Time{}},
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
				Conversation: []CommentEvent{cr(base, meLogin)}, LastCommitAt: base.Add(720 * time.Hour)},
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
	needsReviewerConv := []CommentEvent{at(base.Add(-2*time.Hour), meLogin), at(base.Add(-time.Hour), "author")}
	waitingConv := []CommentEvent{at(base.Add(-2*time.Hour), "author"), at(base.Add(-time.Hour), meLogin)}
	// noneConv never mentions me, so classifyReviewStatus returns "".
	noneConv := []CommentEvent{at(base.Add(-2*time.Hour), "author"), at(base.Add(-time.Hour), "thirdparty")}
	// crConv classifies as "Needs Reviewer" AND carries an outstanding change
	// request: me spoke, submitted the change request at base, and a third party
	// (not the PR author, so D2 cannot fire) spoke last. The change request must
	// not be the final event, or me would be the last speaker and the classifier
	// would say "Waiting on Author" instead.
	crConv := []CommentEvent{
		at(base.Add(-2*time.Hour), meLogin),
		cr(base, meLogin),
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
			pr: PullRequestDetail{Author: meLogin, Conversation: needsReviewerConv,
				ReviewDecision: reviewChangesRequested, Mergeable: mergeableConflicting},
			wantTarget: statusWIP, wantReason: reasonOwnPR,
		},
		{
			name:       "own PR with no conversation is unconditionally routed to WIP",
			pr:         PullRequestDetail{Author: meLogin},
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
			target, reason := routeTarget(tc.pr, meLogin)
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
		"me-last":               {at(before, "author"), cr(base, meLogin)},
		"other-last-me-present": {at(before, meLogin), cr(base, "reviewer"), at(after, "thirdparty")},
		"other-last-me-absent":  {cr(base, "reviewer"), at(after, "thirdparty")},
		"author-last":           {at(before, meLogin), cr(base, "reviewer"), at(after, "author")},
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
					got, reason := routeTarget(pr, meLogin)
					want := classifyReviewStatus(pr.Conversation, meLogin)
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

// signalExtras is the pr/11..pr/19 fixture table exercising the new signals.
func signalExtras(f *routingFixture) {
	base := signalBase
	f.extraPRs = []PullRequestDetail{
		// Conflict pins a "Needs Reviewer" verdict to "Waiting on Author".
		{URL: prURL(11), Author: "author", Mergeable: mergeableConflicting,
			Conversation: []CommentEvent{at(base, meLogin), at(base.Add(time.Hour), "author")}},
		// A11: UNKNOWN is no signal, so plain last-commenter routing applies.
		{URL: prURL(12), Author: "author", Mergeable: "UNKNOWN",
			Conversation: []CommentEvent{at(base, meLogin), at(base.Add(time.Hour), "author")}},
		// The reported G2 gap: a third party spoke after the change request.
		{URL: prURL(13), Author: "author", ReviewDecision: reviewChangesRequested,
			Conversation: []CommentEvent{cr(base, meLogin), at(base.Add(time.Hour), "thirdparty")}},
		// D2: the author replied, so the change request is addressed.
		{URL: prURL(14), Author: "author", ReviewDecision: reviewChangesRequested,
			Conversation: []CommentEvent{cr(base, meLogin), at(base.Add(time.Hour), "author")}},
		// D1: a newer commit addresses the change request.
		{URL: prURL(15), Author: "author", ReviewDecision: reviewChangesRequested,
			LastCommitAt: base.Add(2 * time.Hour),
			Conversation: []CommentEvent{cr(base, meLogin), at(base.Add(time.Hour), "thirdparty")}},
		// Monotonicity: matthyx never spoke, so no signal may create a candidate.
		{URL: prURL(16), Author: "author", Mergeable: mergeableConflicting,
			ReviewDecision: reviewChangesRequested, Conversation: []CommentEvent{}},
		// Stale approved label: reported, never mutated.
		{URL: prURL(17), Author: "author", ReviewDecision: reviewApproved,
			Conversation: []CommentEvent{at(base, meLogin), at(base.Add(time.Hour), "author")}},
		// Already in the decided column: no mutation, but a keeping line and a
		// by-conflict increment. This is the row that pins the counter/line 1:1
		// invariant - without it the correspondence is vacuously satisfied.
		{URL: prURL(18), Author: "author", Mergeable: mergeableConflicting,
			Conversation: []CommentEvent{at(base, meLogin), at(base.Add(time.Hour), "author")}},
		// Reviewer-generic: the change request is dakshhhhh16's. The leading
		// meLogin event exists only to open the P1 gate (the classifier needs me
		// to have participated); it changes nothing about which reviewer's change
		// request newestChangeRequestAt finds.
		{URL: prURL(19), Author: "author", ReviewDecision: reviewChangesRequested,
			Conversation: []CommentEvent{
				at(base.Add(-time.Hour), meLogin),
				cr(base, "dakshhhhh16"),
				at(base.Add(time.Hour), "thirdparty"),
			}},
	}
	f.extraItems = []ProjectItem{
		{ID: "pr-11", URL: prURL(11)},
		{ID: "pr-12", URL: prURL(12)},
		{ID: "pr-13", URL: prURL(13)},
		{ID: "pr-14", URL: prURL(14)},
		{ID: "pr-15", URL: prURL(15)},
		{ID: "pr-16", URL: prURL(16)},
		{ID: "pr-17", URL: prURL(17), StatusName: statusNeedsReviewer},
		{ID: "pr-18", URL: prURL(18), StatusName: statusWaitingOnAuthor},
		{ID: "pr-19", URL: prURL(19)},
	}
}

func TestRunRoutesPRsBySignals(t *testing.T) {
	ctx := t.Context()
	f := newRoutingFixture()
	signalExtras(f)

	out := captureOutput(t, func() { Run(ctx, f.client()) })

	proj := "project-" + prTrackingBoard
	want := []fieldUpdate{
		// Base fixture.
		{proj, "pr-1", "field-123", "option-waiting"},
		{proj, "pr-2", "field-123", "option-needs"},
		{proj, "pr-4", "field-123", "option-archive"},
		{proj, "pr-7", "field-123", "option-waiting"},
		{proj, "pr-8", "field-123", "option-waiting"},
		{proj, "pr-9", "field-123", "option-wip"},
		{proj, "pr-10", "field-123", "option-waiting"},
		// The six extras that produce a move.
		{proj, "pr-11", "field-123", "option-waiting"},
		{proj, "pr-12", "field-123", "option-needs"},
		{proj, "pr-13", "field-123", "option-waiting"},
		{proj, "pr-14", "field-123", "option-needs"},
		{proj, "pr-15", "field-123", "option-needs"},
		{proj, "pr-19", "field-123", "option-waiting"},
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
	// pr/16, pr/17 and pr/18 must never be written to.
	for _, id := range []string{"pr-16", "pr-17", "pr-18"} {
		for _, u := range got {
			if u.itemID == id {
				t.Errorf("%s must produce no mutation, got %+v", id, u)
			}
		}
	}

	wantLines := []string{
		`moved pr ` + prURL(11) + ` from "(none)" to "Waiting on Author" (merge-conflict)`,
		`moved pr ` + prURL(12) + ` from "(none)" to "Needs Reviewer" (last-commenter)`,
		`moved pr ` + prURL(13) + ` from "(none)" to "Waiting on Author" (changes-requested)`,
		`moved pr ` + prURL(19) + ` from "(none)" to "Waiting on Author" (changes-requested)`,
		`keeping pr ` + prURL(18) + ` in "Waiting on Author" (merge-conflict)`,
		"2 by-conflict",
		"2 by-changes-requested",
	}
	for _, w := range wantLines {
		if !strings.Contains(out, w) {
			t.Errorf("expected output to contain %q, got:\n%s", w, out)
		}
	}
	// The keeping line is printed exactly once, and never for last-commenter.
	if n := strings.Count(out, "keeping pr "); n != 1 {
		t.Errorf("expected exactly 1 keeping line, got %d:\n%s", n, out)
	}
	// pr/3 is already in target with reason last-commenter, so it must stay
	// silent: the keeping line is scoped to the two new signals only.
	if strings.Contains(out, "keeping pr "+prURL(3)) {
		t.Errorf("keeping must not print for last-commenter decisions, got:\n%s", out)
	}
	// pr/16 produces no routing decision at all: no move, no keeping line.
	// (It still appears in the console report section, which lists every PR
	// and is not part of the routing output.)
	for _, prefix := range []string{"moved pr ", "would move pr ", "keeping pr "} {
		if strings.Contains(out, prefix+prURL(16)) {
			t.Errorf("pr/16 must produce no %q line, got:\n%s", prefix, out)
		}
	}
	// The counter invariant: each by-* count equals its moved + keeping lines.
	conflictLines := strings.Count(out, "(merge-conflict)")
	crLines := strings.Count(out, "(changes-requested)")
	if conflictLines != 2 {
		t.Errorf("expected 2 lines carrying (merge-conflict) to match by-conflict: 2, got %d:\n%s", conflictLines, out)
	}
	if crLines != 2 {
		t.Errorf("expected 2 lines carrying (changes-requested) to match by-changes-requested: 2, got %d:\n%s", crLines, out)
	}
	// pr/3, pr/17 and pr/18 are all already in target.
	if !strings.Contains(out, "3 already-in-target") {
		t.Errorf("expected 3 already-in-target in summary, got:\n%s", out)
	}
}

func TestRunRoutesApprovedAndDraftAndOwnPRs(t *testing.T) {
	ctx := t.Context()
	f := newRoutingFixture()

	out := captureOutput(t, func() { Run(ctx, f.client()) })

	proj := "project-" + prTrackingBoard
	// pr-7 (APPROVED) is routed to Waiting on Author
	// pr-8 (Draft) is routed to Waiting on Author
	// pr-9 (Author: matthyx) is routed to WIP
	want := []fieldUpdate{
		{proj, "pr-7", "field-123", "option-waiting"},
		{proj, "pr-8", "field-123", "option-waiting"},
		{proj, "pr-9", "field-123", "option-wip"},
	}
	got := f.recorded()
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("missing expected mutation %+v, got %+v", w, got)
		}
	}

	if !strings.Contains(out, `moved pr `+prURL(7)+` from "(none)" to "Waiting on Author" (last-commenter)`) {
		t.Errorf("expected pr/7 (approved) to be routed, got:\n%s", out)
	}
	if !strings.Contains(out, `moved pr `+prURL(8)+` from "(none)" to "Waiting on Author" (last-commenter)`) {
		t.Errorf("expected pr/8 (draft) to be routed, got:\n%s", out)
	}
	if !strings.Contains(out, `moved pr `+prURL(9)+` from "(none)" to "WIP" (own-pr)`) {
		t.Errorf("expected pr/9 (own pr) to be routed to WIP, got:\n%s", out)
	}
}

func TestParsePRRepoAndNumber(t *testing.T) {
	tests := []struct {
		url      string
		wantRepo string
		wantNum  string
		wantOK   bool
	}{
		{
			url:      "https://github.com/kubescape/node-agent/pull/808",
			wantRepo: "node-agent",
			wantNum:  "808",
			wantOK:   true,
		},
		{
			url:      "https://github.com/matthyx/triage-tool/pull/42",
			wantRepo: "triage-tool",
			wantNum:  "42",
			wantOK:   true,
		},
		{
			url:      "https://github.com/kubescape/node-agent/issues/808",
			wantRepo: "",
			wantNum:  "",
			wantOK:   false,
		},
		{
			url:      "invalid-url",
			wantRepo: "",
			wantNum:  "",
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		repo, num, ok := parsePRRepoAndNumber(tt.url)
		if repo != tt.wantRepo || num != tt.wantNum || ok != tt.wantOK {
			t.Errorf("parsePRRepoAndNumber(%q) = (%q, %q, %v); want (%q, %q, %v)",
				tt.url, repo, num, ok, tt.wantRepo, tt.wantNum, tt.wantOK)
		}
	}
}

func TestMulticaIssueInvocation(t *testing.T) {
	var calledWith []string
	orig := createMulticaIssue
	defer func() { createMulticaIssue = orig }()

	createMulticaIssue = func(ctx context.Context, repoName, prNumber, fullURL string) error {
		title := fmt.Sprintf("%s %s", repoName, prNumber)
		description := fmt.Sprintf("review %s add PR comments on blockers, when it's good to merge approve", fullURL)
		calledWith = []string{repoName, prNumber, fullURL, title, description}
		return nil
	}

	url := "https://github.com/kubescape/node-agent/pull/808"
	repo, num, ok := parsePRRepoAndNumber(url)
	if !ok {
		t.Fatalf("failed to parse url")
	}
	err := createMulticaIssue(t.Context(), repo, num, url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(calledWith) != 5 {
		t.Fatalf("expected 5 elements in calledWith, got %d", len(calledWith))
	}

	if calledWith[3] != "node-agent 808" {
		t.Errorf("expected title 'node-agent 808', got %q", calledWith[3])
	}

	expectedDesc := "review https://github.com/kubescape/node-agent/pull/808 add PR comments on blockers, when it's good to merge approve"
	if calledWith[4] != expectedDesc {
		t.Errorf("expected description %q, got %q", expectedDesc, calledWith[4])
	}
}

