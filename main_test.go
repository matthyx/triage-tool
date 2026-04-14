package main

import (
	"context"
	"sync"
	"testing"

	mapset "github.com/deckarep/golang-set/v2"
)

type MockGHClient struct {
	AddProjectItemFunc              func(ctx context.Context, owner, board, url string) error
	GetProjectIDFunc                func(ctx context.Context, owner, board string) (string, error)
	GetContentIDFunc                func(ctx context.Context, url string) (string, error)
	AddProjectItemWithIDsFunc       func(ctx context.Context, projectID, contentID string) error
	GetProjectItemsWithStateFunc    func(ctx context.Context, owner, board string, limit int) ([]ProjectItem, error)
	GetBothProjectItemsFunc         func(ctx context.Context, owner, bugBoard, prBoard string, limit int) (mapset.Set[string], mapset.Set[string], error)
	GetIssuesAndPullsFunc           func(ctx context.Context, repo string, limit int) ([]string, []string, error)
	GetRepositoriesFunc             func(ctx context.Context, owner string, limit int) ([]string, error)
	GetToArchiveFieldOptionFunc     func(ctx context.Context, owner, board string) (string, string, error)
	UpdateProjectItemFieldFunc      func(ctx context.Context, projectID, itemID, fieldID, optionID string) error
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

func (m *MockGHClient) GetIssuesAndPulls(ctx context.Context, repo string, limit int) ([]string, []string, error) {
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
	ctx := context.Background()
	var mu sync.Mutex
	addedByProject := map[string]int{}

	mockClient := &MockGHClient{
		GetRepositoriesFunc: func(ctx context.Context, owner string, limit int) ([]string, error) {
			return []string{"kubescape/repo1"}, nil
		},
		GetIssuesAndPullsFunc: func(ctx context.Context, repo string, limit int) ([]string, []string, error) {
			return []string{"https://github.com/kubescape/repo1/issues/1"}, []string{"https://github.com/kubescape/repo1/pull/1"}, nil
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
	if addedByProject["project-"+prTrackingBoard] != 1 {
		t.Errorf("expected 1 added pull, got %d", addedByProject["project-"+prTrackingBoard])
	}
}
