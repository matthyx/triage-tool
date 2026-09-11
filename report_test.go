package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAttentionListsSorting(t *testing.T) {
	ctx := t.Context()
	now := time.Now()

	mockClient := &MockGHClient{
		GetRepositoriesFunc: func(ctx context.Context, owner string, limit int) ([]string, error) {
			return []string{"kubescape/repo1"}, nil
		},
		GetIssuesAndPullsFunc: func(ctx context.Context, repo string, limit int) ([]string, []PullRequestDetail, error) {
			return nil, []PullRequestDetail{
				// Failing CI PRs in reverse chronological order
				{
					URL:        "https://github.com/kubescape/repo1/pull/1",
					Title:      "Failing CI newer (2 days ago)",
					Repository: repo,
					CIState:    "FAILURE",
					UpdatedAt:  now.Add(-2 * 24 * time.Hour),
				},
				{
					URL:        "https://github.com/kubescape/repo1/pull/2",
					Title:      "Failing CI older (5 days ago)",
					Repository: repo,
					CIState:    "FAILURE",
					UpdatedAt:  now.Add(-5 * 24 * time.Hour),
				},
				// Approved PRs in reverse chronological order
				{
					URL:            "https://github.com/kubescape/repo1/pull/3",
					Title:          "Approved newer (1 day ago)",
					Repository:     repo,
					CIState:        "SUCCESS",
					ReviewDecision: "APPROVED",
					Mergeable:      mergeableMergeable,
					UpdatedAt:      now.Add(-1 * 24 * time.Hour),
				},
				{
					URL:            "https://github.com/kubescape/repo1/pull/4",
					Title:          "Approved older (4 days ago)",
					Repository:     repo,
					CIState:        "SUCCESS",
					ReviewDecision: "APPROVED",
					Mergeable:      mergeableMergeable,
					UpdatedAt:      now.Add(-4 * 24 * time.Hour),
				},
				// Stale / Waiting PRs in reverse chronological order
				{
					URL:            "https://github.com/kubescape/repo1/pull/5",
					Title:          "Stale newer (8 days ago)",
					Repository:     repo,
					CIState:        "SUCCESS",
					ReviewDecision: "REVIEW_REQUIRED",
					UpdatedAt:      now.Add(-8 * 24 * time.Hour),
				},
				{
					URL:            "https://github.com/kubescape/repo1/pull/6",
					Title:          "Stale older (12 days ago)",
					Repository:     repo,
					CIState:        "SUCCESS",
					ReviewDecision: "REVIEW_REQUIRED",
					UpdatedAt:      now.Add(-12 * 24 * time.Hour),
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
			return "item-123", nil
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
			return nil
		},
	}

	out := captureOutput(t, func() { Run(ctx, mockClient) })

	attnSectionIdx := strings.Index(out, "PULL REQUESTS NEEDING ATTENTION")
	if attnSectionIdx == -1 {
		t.Fatalf("missing PULL REQUESTS NEEDING ATTENTION header in output")
	}
	attnOut := out[attnSectionIdx:]

	// Check FAILING CI/CD ordering: older (pull/2) should appear before newer (pull/1)
	idxFailOld := strings.Index(attnOut, "https://github.com/kubescape/repo1/pull/2")
	idxFailNew := strings.Index(attnOut, "https://github.com/kubescape/repo1/pull/1")
	if idxFailOld == -1 || idxFailNew == -1 || idxFailOld >= idxFailNew {
		t.Errorf("expected failing CI older PR (pull/2) before newer PR (pull/1), got idxOld=%d idxNew=%d", idxFailOld, idxFailNew)
	}

	// Check APPROVED ordering: older (pull/4) should appear before newer (pull/3)
	idxAppOld := strings.Index(attnOut, "https://github.com/kubescape/repo1/pull/4")
	idxAppNew := strings.Index(attnOut, "https://github.com/kubescape/repo1/pull/3")
	if idxAppOld == -1 || idxAppNew == -1 || idxAppOld >= idxAppNew {
		t.Errorf("expected approved older PR (pull/4) before newer PR (pull/3), got idxOld=%d idxNew=%d", idxAppOld, idxAppNew)
	}

	// Check STALE ordering: older (pull/6) should appear before newer (pull/5)
	idxStaleOld := strings.Index(attnOut, "https://github.com/kubescape/repo1/pull/6")
	idxStaleNew := strings.Index(attnOut, "https://github.com/kubescape/repo1/pull/5")
	if idxStaleOld == -1 || idxStaleNew == -1 || idxStaleOld >= idxStaleNew {
		t.Errorf("expected stale older PR (pull/6) before newer PR (pull/5), got idxOld=%d idxNew=%d", idxStaleOld, idxStaleNew)
	}
}

func TestApprovedPRWithMergeConflictExcludedFromReadyToMerge(t *testing.T) {
	ctx := t.Context()
	now := time.Now()

	mockClient := &MockGHClient{
		GetRepositoriesFunc: func(ctx context.Context, owner string, limit int) ([]string, error) {
			return []string{"kubescape/repo1"}, nil
		},
		GetIssuesAndPullsFunc: func(ctx context.Context, repo string, limit int) ([]string, []PullRequestDetail, error) {
			return nil, []PullRequestDetail{
				{
					URL:            "https://github.com/kubescape/repo1/pull/1",
					Title:          "Approved clean PR",
					Repository:     repo,
					CIState:        "SUCCESS",
					ReviewDecision: "APPROVED",
					Mergeable:      "MERGEABLE",
					UpdatedAt:      now.Add(-1 * 24 * time.Hour),
				},
				{
					URL:            "https://github.com/kubescape/repo1/pull/2",
					Title:          "Approved but conflicting PR",
					Repository:     repo,
					CIState:        "SUCCESS",
					ReviewDecision: "APPROVED",
					Mergeable:      mergeableConflicting,
					UpdatedAt:      now.Add(-1 * 24 * time.Hour),
				},
				{
					URL:            "https://github.com/kubescape/repo1/pull/3",
					Title:          "Approved but unknown mergeability PR",
					Repository:     repo,
					CIState:        "SUCCESS",
					ReviewDecision: "APPROVED",
					Mergeable:      "UNKNOWN",
					UpdatedAt:      now.Add(-1 * 24 * time.Hour),
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
			return "item-123", nil
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
			return nil
		},
	}

	out := captureOutput(t, func() { Run(ctx, mockClient) })

	attnSectionIdx := strings.Index(out, "APPROVED & READY TO MERGE:")
	if attnSectionIdx == -1 {
		t.Fatalf("missing APPROVED & READY TO MERGE header in output")
	}
	attnOut := out[attnSectionIdx:]

	if !strings.Contains(attnOut, "https://github.com/kubescape/repo1/pull/1") {
		t.Errorf("expected clean approved PR (pull/1) in APPROVED & READY TO MERGE section, output:\n%s", attnOut)
	}
	if strings.Contains(attnOut, "https://github.com/kubescape/repo1/pull/2") {
		t.Errorf("expected conflicting approved PR (pull/2) to be excluded from APPROVED & READY TO MERGE section, output:\n%s", attnOut)
	}
	if strings.Contains(attnOut, "https://github.com/kubescape/repo1/pull/3") {
		t.Errorf("expected unknown mergeability approved PR (pull/3) to be excluded from APPROVED & READY TO MERGE section, output:\n%s", attnOut)
	}
}
