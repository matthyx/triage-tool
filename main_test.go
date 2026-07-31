package main

import (
	"context"
	"slices"
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
