package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

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
		AddProjectItemWithIDsFunc: func(ctx context.Context, projectID, contentID string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			addedByProject[projectID]++
			return "item-" + contentID, nil
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
		AddProjectItemWithIDsFunc: func(ctx context.Context, projectID, contentID string) (string, error) {
			return "item-123", nil
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
		"2 by-conflict",
		"2 by-changes-requested",
	}
	for _, w := range wantLines {
		if !strings.Contains(out, w) {
			t.Errorf("expected output to contain %q, got:\n%s", w, out)
		}
	}
	// The keeping line should never be printed.
	if strings.Contains(out, "keeping pr ") {
		t.Errorf("expected no keeping lines, got:\n%s", out)
	}
	// pr/16 produces no routing decision at all: no move line.
	// (It still appears in the console report section, which lists every PR
	// and is not part of the routing output.)
	for _, prefix := range []string{"moved pr ", "would move pr "} {
		if strings.Contains(out, prefix+prURL(16)) {
			t.Errorf("pr/16 must produce no %q line, got:\n%s", prefix, out)
		}
	}
	conflictLines := strings.Count(out, "(merge-conflict)")
	crLines := strings.Count(out, "(changes-requested)")
	if conflictLines != 1 {
		t.Errorf("expected 1 line carrying (merge-conflict), got %d:\n%s", conflictLines, out)
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

func TestUntrackedPRRoutingAndMultica(t *testing.T) {
	ctx := t.Context()
	var mu sync.Mutex
	var multicaCreated []string
	var updates []fieldUpdate

	orig := createMulticaIssue
	defer func() { createMulticaIssue = orig }()

	createMulticaIssue = func(ctx context.Context, repoName, prNumber, fullURL string) error {
		mu.Lock()
		defer mu.Unlock()
		multicaCreated = append(multicaCreated, fullURL)
		return nil
	}

	teamPRURL := "https://github.com/kubescape/node-agent/pull/936"
	externalPRURL := "https://github.com/kubescape/node-agent/pull/937"

	mockClient := &MockGHClient{
		GetRepositoriesFunc: func(ctx context.Context, owner string, limit int) ([]string, error) {
			return []string{"kubescape/node-agent"}, nil
		},
		GetIssuesAndPullsFunc: func(ctx context.Context, repo string, limit int) ([]string, []PullRequestDetail, error) {
			return nil, []PullRequestDetail{
				{
					URL:        teamPRURL,
					Title:      "Team PR",
					Repository: repo,
					Author:     "matthyx",
				},
				{
					URL:        externalPRURL,
					Title:      "External PR",
					Repository: repo,
					Author:     "external-user",
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
			return "content-" + url, nil
		},
		AddProjectItemWithIDsFunc: func(ctx context.Context, projectID, contentID string) (string, error) {
			return "item-" + contentID, nil
		},
		AddProjectItemFunc: func(ctx context.Context, owner, board, url string) error {
			return nil
		},
		GetToArchiveFieldOptionFunc: func(ctx context.Context, owner, board string) (string, string, error) {
			return "field-123", "option-archive", nil
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

	out := captureOutput(t, func() { Run(ctx, mockClient) })

	// Check multica issue creation: only external PR should trigger multica
	if slices.Contains(multicaCreated, teamPRURL) {
		t.Errorf("team PR %s should not have created a multica issue", teamPRURL)
	}
	if !slices.Contains(multicaCreated, externalPRURL) {
		t.Errorf("external PR %s should have created a multica issue", externalPRURL)
	}

	// Check routing: newly added team PR must be moved to WIP
	wantWIPUpdate := fieldUpdate{
		projectID: "project-" + prTrackingBoard,
		itemID:    "item-content-" + teamPRURL,
		fieldID:   "field-123",
		optionID:  "option-wip",
	}
	if !slices.Contains(updates, wantWIPUpdate) {
		t.Errorf("missing expected WIP update %+v, got %+v", wantWIPUpdate, updates)
	}
	if !strings.Contains(out, "moved pr "+teamPRURL+` from "(none)" to "WIP" (own-pr)`) {
		t.Errorf("expected output to mention moving team PR to WIP, got:\n%s", out)
	}
}

func TestRunBoardFetchError(t *testing.T) {
	ctx := t.Context()
	mockClient := &MockGHClient{
		GetRepositoriesFunc: func(ctx context.Context, owner string, limit int) ([]string, error) {
			return []string{"kubescape/repo1"}, nil
		},
		GetIssuesAndPullsFunc: func(ctx context.Context, repo string, limit int) ([]string, []PullRequestDetail, error) {
			return nil, nil, nil
		},
		GetProjectItemsWithStateFunc: func(ctx context.Context, owner, board string, limit int) ([]ProjectItem, error) {
			return nil, errors.New("board query failed")
		},
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on board fetch error, but did not panic")
		}
	}()

	Run(ctx, mockClient)
}
