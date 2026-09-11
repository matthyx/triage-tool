package main

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
)

func parsePRRepoAndNumber(urlStr string) (repo string, number string, ok bool) {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(parsedURL.Path, "/")
	if len(parts) < 5 || parts[3] != "pull" {
		return "", "", false
	}
	return parts[2], parts[4], true
}

var createMulticaIssue = defaultCreateMulticaIssue

func defaultCreateMulticaIssue(ctx context.Context, repoName, prNumber, fullURL string) error {
	title := fmt.Sprintf("%s %s", repoName, prNumber)
	description, err := renderPRReviewPrompt(fullURL)
	if err != nil {
		return fmt.Errorf("render PR review prompt: %w", err)
	}

	cmd := exec.CommandContext(ctx, "multica", "issue", "create",
		"--assignee", "Codex",
		"--title", title,
		"--description", description,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("multica execution failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}
