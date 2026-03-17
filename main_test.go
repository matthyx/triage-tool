package main

import (
	"context"
	"sync"
	"testing"

	mapset "github.com/deckarep/golang-set/v2"
)

type MockGHClient struct {
	AddProjectItemFunc        func(ctx context.Context, owner, board, url string) error
	GetProjectIDFunc          func(ctx context.Context, owner, board string) (string, error)
	GetContentIDFunc          func(ctx context.Context, url string) (string, error)
	AddProjectItemWithIDsFunc func(ctx context.Context, projectID, contentID string) error
	GetProjectItemsFunc       func(ctx context.Context, owner, board string, limit int) (mapset.Set[string], error)
	GetBothProjectItemsFunc   func(ctx context.Context, owner, bugBoard, prBoard string, limit int) (mapset.Set[string], mapset.Set[string], error)
	GetIssuesAndPullsFunc     func(ctx context.Context, repo string, limit int) ([]string, []string, error)
	GetRepositoriesFunc       func(ctx context.Context, owner string, limit int) ([]string, error)
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

func (m *MockGHClient) GetProjectItems(ctx context.Context, owner, board string, limit int) (mapset.Set[string], error) {
	return m.GetProjectItemsFunc(ctx, owner, board, limit)
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

func TestRun(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	addedIssues := []string{}
	addedPulls := []string{}

	mockClient := &MockGHClient{
		GetRepositoriesFunc: func(ctx context.Context, owner string, limit int) ([]string, error) {
			return []string{"kubescape/repo1"}, nil
		},
		GetIssuesAndPullsFunc: func(ctx context.Context, repo string, limit int) ([]string, []string, error) {
			return []string{"https://github.com/kubescape/repo1/issues/1"}, []string{"https://github.com/kubescape/repo1/pull/1"}, nil
		},
		GetProjectItemsFunc: func(ctx context.Context, owner, board string, limit int) (mapset.Set[string], error) {
			return mapset.NewSet[string](), nil
		},
		GetProjectIDFunc: func(ctx context.Context, owner, board string) (string, error) {
			return "project-123", nil
		},
		GetContentIDFunc: func(ctx context.Context, url string) (string, error) {
			return "content-123", nil
		},
		AddProjectItemWithIDsFunc: func(ctx context.Context, projectID, contentID string) error {
			return nil
		},
		AddProjectItemFunc: func(ctx context.Context, owner, board, url string) error {
			mu.Lock()
			defer mu.Unlock()
			if board == bugTrackingBoard {
				addedIssues = append(addedIssues, url)
			} else if board == prTrackingBoard {
				addedPulls = append(addedPulls, url)
			}
			return nil
		},
	}

	Run(ctx, mockClient)

	if len(addedIssues) != 1 {
		t.Errorf("expected 1 added issue, got %d", len(addedIssues))
	}
	if len(addedPulls) != 1 {
		t.Errorf("expected 1 added pull, got %d", len(addedPulls))
	}
}
