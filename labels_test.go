package main

import (
	"context"
	"strings"
	"sync"
	"testing"
)

func TestPullRequestDetail_LabelHelpers(t *testing.T) {
	pr := PullRequestDetail{
		Labels: []Label{
			{ID: "lbl-1", Name: "bug"},
			{ID: "lbl-2", Name: labelNeedsRebase},
		},
	}

	if !pr.HasLabel(labelNeedsRebase) {
		t.Errorf("expected HasLabel(%q) to be true", labelNeedsRebase)
	}
	if pr.HasLabel("feature") {
		t.Errorf("expected HasLabel(%q) to be false", "feature")
	}

	if got := pr.LabelID(labelNeedsRebase); got != "lbl-2" {
		t.Errorf("expected LabelID(%q) = %q, got %q", labelNeedsRebase, "lbl-2", got)
	}
	if got := pr.LabelID("nonexistent"); got != "" {
		t.Errorf("expected LabelID(%q) = empty, got %q", "nonexistent", got)
	}
}

func TestSyncNeedsRebaseLabels_AddAndRemove(t *testing.T) {
	ctx := t.Context()

	var mu sync.Mutex
	var addedLabels []struct{ prID, labelID string }
	var removedLabels []struct{ prID, labelID string }
	var ensuredLabels []string

	mockClient := &MockGHClient{
		EnsureRepoLabelFunc: func(ctx context.Context, repo, name, color, description string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			ensuredLabels = append(ensuredLabels, repo+":"+name)
			return "lbl-id-needs-rebase", nil
		},
		AddLabelToPRFunc: func(ctx context.Context, prID, labelID string) error {
			mu.Lock()
			defer mu.Unlock()
			addedLabels = append(addedLabels, struct{ prID, labelID string }{prID, labelID})
			return nil
		},
		RemoveLabelFromPRFunc: func(ctx context.Context, prID, labelID string) error {
			mu.Lock()
			defer mu.Unlock()
			removedLabels = append(removedLabels, struct{ prID, labelID string }{prID, labelID})
			return nil
		},
	}

	prs := map[string]PullRequestDetail{
		// 1. Conflicting and no label -> should add
		"https://github.com/kubescape/repo1/pull/1": {
			ID:         "pr-1",
			URL:        "https://github.com/kubescape/repo1/pull/1",
			Repository: "kubescape/repo1",
			Mergeable:  mergeableConflicting,
			Labels:     []Label{},
		},
		// 2. Conflicting and already has label -> nothing to do
		"https://github.com/kubescape/repo1/pull/2": {
			ID:         "pr-2",
			URL:        "https://github.com/kubescape/repo1/pull/2",
			Repository: "kubescape/repo1",
			Mergeable:  mergeableConflicting,
			Labels:     []Label{{ID: "lbl-existing", Name: labelNeedsRebase}},
		},
		// 3. Mergeable and has label -> should remove
		"https://github.com/kubescape/repo1/pull/3": {
			ID:         "pr-3",
			URL:        "https://github.com/kubescape/repo1/pull/3",
			Repository: "kubescape/repo1",
			Mergeable:  mergeableMergeable,
			Labels:     []Label{{ID: "lbl-3", Name: labelNeedsRebase}},
		},
		// 4. Mergeable and no label -> nothing to do
		"https://github.com/kubescape/repo1/pull/4": {
			ID:         "pr-4",
			URL:        "https://github.com/kubescape/repo1/pull/4",
			Repository: "kubescape/repo1",
			Mergeable:  mergeableMergeable,
			Labels:     []Label{},
		},
		// 5. UNKNOWN mergeability and no label -> no signal, do nothing
		"https://github.com/kubescape/repo1/pull/5": {
			ID:         "pr-5",
			URL:        "https://github.com/kubescape/repo1/pull/5",
			Repository: "kubescape/repo1",
			Mergeable:  "UNKNOWN",
			Labels:     []Label{},
		},
		// 6. UNKNOWN mergeability and has label -> no signal, do nothing
		"https://github.com/kubescape/repo1/pull/6": {
			ID:         "pr-6",
			URL:        "https://github.com/kubescape/repo1/pull/6",
			Repository: "kubescape/repo1",
			Mergeable:  "UNKNOWN",
			Labels:     []Label{{ID: "lbl-6", Name: labelNeedsRebase}},
		},
	}

	out := captureOutput(t, func() {
		syncNeedsRebaseLabels(ctx, mockClient, prs)
	})

	if len(addedLabels) != 1 {
		t.Fatalf("expected 1 added label, got %d (%+v)", len(addedLabels), addedLabels)
	}
	if addedLabels[0].prID != "pr-1" || addedLabels[0].labelID != "lbl-id-needs-rebase" {
		t.Errorf("unexpected added label: %+v", addedLabels[0])
	}

	if len(removedLabels) != 1 {
		t.Fatalf("expected 1 removed label, got %d (%+v)", len(removedLabels), removedLabels)
	}
	if removedLabels[0].prID != "pr-3" || removedLabels[0].labelID != "lbl-3" {
		t.Errorf("unexpected removed label: %+v", removedLabels[0])
	}

	if !strings.Contains(out, `added "needs-rebase" label to https://github.com/kubescape/repo1/pull/1`) {
		t.Errorf("expected log for added label, got:\n%s", out)
	}
	if !strings.Contains(out, `removed "needs-rebase" label from https://github.com/kubescape/repo1/pull/3`) {
		t.Errorf("expected log for removed label, got:\n%s", out)
	}
	if !strings.Contains(out, "labels: 1 added, 1 removed") {
		t.Errorf("expected summary line 'labels: 1 added, 1 removed', got:\n%s", out)
	}
}

func TestSyncNeedsRebaseLabels_DryRun(t *testing.T) {
	t.Setenv("TRIAGE_ROUTING_DRY_RUN", "1")
	ctx := t.Context()

	mockClient := &MockGHClient{
		EnsureRepoLabelFunc: func(ctx context.Context, repo, name, color, description string) (string, error) {
			t.Fatal("EnsureRepoLabel must not be called during dry run")
			return "", nil
		},
		AddLabelToPRFunc: func(ctx context.Context, prID, labelID string) error {
			t.Fatal("AddLabelToPR must not be called during dry run")
			return nil
		},
		RemoveLabelFromPRFunc: func(ctx context.Context, prID, labelID string) error {
			t.Fatal("RemoveLabelFromPR must not be called during dry run")
			return nil
		},
	}

	prs := map[string]PullRequestDetail{
		"https://github.com/kubescape/repo1/pull/1": {
			ID:         "pr-1",
			URL:        "https://github.com/kubescape/repo1/pull/1",
			Repository: "kubescape/repo1",
			Mergeable:  mergeableConflicting,
			Labels:     []Label{},
		},
		"https://github.com/kubescape/repo1/pull/3": {
			ID:         "pr-3",
			URL:        "https://github.com/kubescape/repo1/pull/3",
			Repository: "kubescape/repo1",
			Mergeable:  mergeableMergeable,
			Labels:     []Label{{ID: "lbl-3", Name: labelNeedsRebase}},
		},
	}

	out := captureOutput(t, func() {
		syncNeedsRebaseLabels(ctx, mockClient, prs)
	})

	if !strings.Contains(out, `would add "needs-rebase" label to https://github.com/kubescape/repo1/pull/1`) {
		t.Errorf("expected would-add line, got:\n%s", out)
	}
	if !strings.Contains(out, `would remove "needs-rebase" label from https://github.com/kubescape/repo1/pull/3`) {
		t.Errorf("expected would-remove line, got:\n%s", out)
	}
	if !strings.Contains(out, "labels: 1 would-add, 1 would-remove") {
		t.Errorf("expected dry run summary line, got:\n%s", out)
	}
}

func TestSyncNeedsRebaseLabels_FallbackURLParsingAndContentID(t *testing.T) {
	ctx := t.Context()

	var addedPRID, addedLabelID string
	var getContentIDCalls int

	mockClient := &MockGHClient{
		GetContentIDFunc: func(ctx context.Context, url string) (string, error) {
			getContentIDCalls++
			return "content-id-from-url", nil
		},
		EnsureRepoLabelFunc: func(ctx context.Context, repo, name, color, description string) (string, error) {
			if repo != "kubescape/repo1" {
				t.Errorf("expected parsed repo kubescape/repo1, got %s", repo)
			}
			return "lbl-id-1", nil
		},
		AddLabelToPRFunc: func(ctx context.Context, prID, labelID string) error {
			addedPRID = prID
			addedLabelID = labelID
			return nil
		},
	}

	// PR without explicit ID or Repository, relying on URL parsing & GetContentID fallback
	prs := map[string]PullRequestDetail{
		"https://github.com/kubescape/repo1/pull/42": {
			URL:       "https://github.com/kubescape/repo1/pull/42",
			Mergeable: mergeableConflicting,
		},
	}

	out := captureOutput(t, func() {
		syncNeedsRebaseLabels(ctx, mockClient, prs)
	})

	if getContentIDCalls != 1 {
		t.Errorf("expected 1 GetContentID call, got %d", getContentIDCalls)
	}
	if addedPRID != "content-id-from-url" {
		t.Errorf("expected PR ID content-id-from-url, got %s", addedPRID)
	}
	if addedLabelID != "lbl-id-1" {
		t.Errorf("expected label ID lbl-id-1, got %s", addedLabelID)
	}
	if !strings.Contains(out, `added "needs-rebase" label to https://github.com/kubescape/repo1/pull/42`) {
		t.Errorf("expected output log, got:\n%s", out)
	}
}
