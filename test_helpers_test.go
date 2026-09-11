package main

import (
	"bytes"
	"context"
	"fmt"
	mapset "github.com/deckarep/golang-set/v2"
	"io"
	"os"
	"slices"
	"sync"
	"testing"
	"time"
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
	AddProjectItemWithIDsFunc    func(ctx context.Context, projectID, contentID string) (string, error)
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

func (m *MockGHClient) AddProjectItemWithIDs(ctx context.Context, projectID, contentID string) (string, error) {
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
		{URL: prURL(1), Author: "author", Conversation: conv(human("someone"), human("matthyx"))},
		{URL: prURL(2), Author: "author", Conversation: conv(human("matthyx"), human("author"))},
		{URL: prURL(3), Author: "author", Conversation: conv(human("matthyx"), human("author"))},
		{URL: prURL(4), Author: "author", Conversation: conv(human("someone"), human("matthyx"))},
		{URL: prURL(5), Author: "author", Conversation: conv(human("someone"), human("matthyx"))},
		{URL: prURL(6), Author: "author", Conversation: conv(human("someone"), human("matthyx"))},
		{URL: prURL(7), Author: "author", ReviewDecision: "APPROVED", Conversation: conv(human("someone"), human("matthyx"))},
		{URL: prURL(8), Author: "author", IsDraft: true, Conversation: conv(human("someone"), human("matthyx"))},
		{URL: prURL(9), Author: "matthyx", Conversation: conv(human("someone"), human("matthyx"))},
		{URL: prURL(10), Author: "author", Conversation: conv(human("matthyx"), bot("dependabot"))},
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
		AddProjectItemWithIDsFunc: func(ctx context.Context, projectID, contentID string) (string, error) {
			return "item-123", nil
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

// signalBase is the fixed reference time the signal fixtures hang off, so the
// before/after relationships in them are exact rather than wall-clock dependent.
var signalBase = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

// signalExtras is the pr/11..pr/19 fixture table exercising the new signals.
func signalExtras(f *routingFixture) {
	base := signalBase
	f.extraPRs = []PullRequestDetail{
		// Conflict pins a "Needs Reviewer" verdict to "Waiting on Author".
		{URL: prURL(11), Author: "author", Mergeable: mergeableConflicting,
			Conversation: []CommentEvent{at(base, "matthyx"), at(base.Add(time.Hour), "author")}},
		// A11: UNKNOWN is no signal, so plain last-commenter routing applies.
		{URL: prURL(12), Author: "author", Mergeable: "UNKNOWN",
			Conversation: []CommentEvent{at(base, "matthyx"), at(base.Add(time.Hour), "author")}},
		// The reported G2 gap: a third party spoke after the change request.
		{URL: prURL(13), Author: "author", ReviewDecision: reviewChangesRequested,
			Conversation: []CommentEvent{cr(base, "matthyx"), at(base.Add(time.Hour), "thirdparty")}},
		// D2: the author replied, so the change request is addressed.
		{URL: prURL(14), Author: "author", ReviewDecision: reviewChangesRequested,
			Conversation: []CommentEvent{cr(base, "matthyx"), at(base.Add(time.Hour), "author")}},
		// D1: a newer commit addresses the change request.
		{URL: prURL(15), Author: "author", ReviewDecision: reviewChangesRequested,
			LastCommitAt: base.Add(2 * time.Hour),
			Conversation: []CommentEvent{cr(base, "matthyx"), at(base.Add(time.Hour), "thirdparty")}},
		// Monotonicity: matthyx never spoke, so no signal may create a candidate.
		{URL: prURL(16), Author: "author", Mergeable: mergeableConflicting,
			ReviewDecision: reviewChangesRequested, Conversation: []CommentEvent{}},
		// Stale approved label: reported, never mutated.
		{URL: prURL(17), Author: "author", ReviewDecision: reviewApproved,
			Conversation: []CommentEvent{at(base, "matthyx"), at(base.Add(time.Hour), "author")}},
		// Already in the decided column: no mutation, but a by-conflict increment.
		{URL: prURL(18), Author: "author", Mergeable: mergeableConflicting,
			Conversation: []CommentEvent{at(base, "matthyx"), at(base.Add(time.Hour), "author")}},
		// Reviewer-generic: the change request is dakshhhhh16's. The leading
		// "matthyx" event exists only to open the P1 gate (the classifier needs me
		// to have participated); it changes nothing about which reviewer's change
		// request newestChangeRequestAt finds.
		{URL: prURL(19), Author: "author", ReviewDecision: reviewChangesRequested,
			Conversation: []CommentEvent{
				at(base.Add(-time.Hour), "matthyx"),
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
