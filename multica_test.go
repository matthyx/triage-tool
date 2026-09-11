package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

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
	dir := t.TempDir()
	capture := filepath.Join(dir, "args")
	t.Setenv("MULTICA_TEST_ARGS", capture)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(dir, "multica"), []byte("#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$MULTICA_TEST_ARGS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	url := "https://github.com/kubescape/node-agent/pull/808"
	if err := defaultCreateMulticaIssue(t.Context(), "node-agent", "808", url); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	wantPrefix := []string{"issue", "create", "--assignee", "Codex", "--title", "node-agent 808", "--description"}
	if len(args) != 8 || !slices.Equal(args[:7], wantPrefix) {
		t.Fatalf("unexpected command arguments: %q", args)
	}
	wantDescription, err := renderPRReviewPrompt(url)
	if err != nil {
		t.Fatal(err)
	}
	if args[7] != wantDescription || !strings.Contains(args[7], url) || strings.Contains(args[7], "{{.PRURL}}") {
		t.Fatalf("unexpected description: %q", args[7])
	}
}
