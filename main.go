package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/google/go-github/v66/github"
)

const (
	bugTrackingBoard = "4"
	prTrackingBoard  = "5"
)

type ItemListOutput struct {
	Items      []Items `json:"items,omitempty"`
	TotalCount int     `json:"totalCount,omitempty"`
}

type Content struct {
	Body       string `json:"body,omitempty"`
	Number     int    `json:"number,omitempty"`
	Repository string `json:"repository,omitempty"`
	Title      string `json:"title,omitempty"`
	Type       string `json:"type,omitempty"`
	URL        string `json:"url,omitempty"`
}

type Items struct {
	Content    Content  `json:"content,omitempty"`
	ID         string   `json:"id,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	Repository string   `json:"repository,omitempty"`
	Status     string   `json:"status,omitempty"`
	Title      string   `json:"title,omitempty"`
}

type IssueListOutput []struct {
	URL string `json:"url,omitempty"`
}

type RepoListOutput []struct {
	Name string `json:"name,omitempty"`
}

func addProjectItem(owner, board, url string) error {
	cmd := exec.Command("gh", "project", "item-add", board, "--owner", owner, "--url", url)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add project item: %s", out)
	}
	return nil
}

func getProjectItems(owner, board string, limit int) (mapset.Set[string], error) {
	cmd := exec.Command("gh", "project", "item-list", board, "--owner", owner, "-L", strconv.Itoa(limit), "--format", "json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list project items: %s", out)
	}
	var items ItemListOutput
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("failed to unmarshal project items: %w", err)
	}
	urls := mapset.NewSet[string]()
	for _, item := range items.Items {
		urls.Add(item.Content.URL)
	}
	return urls, nil
}

func getIssues(repo string, limit int) ([]string, error) {
	cmd := exec.Command("gh", "issue", "list", "-R", repo, "-L", strconv.Itoa(limit), "--json", "url")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "repository has disabled issues") {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list issues: %s", out)
	}
	var issues IssueListOutput
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("failed to unmarshal repo: %w", err)
	}
	var urls []string
	for _, issue := range issues {
		urls = append(urls, issue.URL)
	}
	return urls, nil
}

func getPulls(ghClient *github.Client, owner, repo string) ([]*github.PullRequest, error) {
	opts := &github.PullRequestListOptions{
		State: "open",
	}
	var allPulls []*github.PullRequest
	for {
		pulls, resp, err := ghClient.PullRequests.List(context.Background(), owner, repo, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to list pull requests: %w", err)
		}
		allPulls = append(allPulls, pulls...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return allPulls, nil
}

func getRepositories(owner string, limit int) ([]string, error) {
	cmd := exec.Command("gh", "repo", "list", owner, "--no-archived", "-L", strconv.Itoa(limit), "--visibility", "public", "--json", "name")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list repo: %s", out)
	}
	var repos RepoListOutput
	if err := json.Unmarshal(out, &repos); err != nil {
		return nil, fmt.Errorf("failed to unmarshal repo: %w", err)
	}
	var names []string
	for _, repo := range repos {
		names = append(names, owner+"/"+repo.Name)
	}
	return names, nil
}

func main() {
	limit := 1000
	repositories, err := getRepositories("kubescape", limit)
	if err != nil {
		panic(err)
	}
	allIssues := mapset.NewSet[string]()
	for _, repo := range repositories {
		issues, err := getIssues(repo, limit)
		if err != nil {
			panic(err)
		}
		allIssues.Append(issues...)
	}
	fmt.Println("issues", allIssues.Cardinality())

	trackedBugs, err := getProjectItems("kubescape", bugTrackingBoard, limit)
	if err != nil {
		panic(err)
	}
	fmt.Println("tracked", trackedBugs.Cardinality())

	untrackedBugs := allIssues.Difference(trackedBugs)
	fmt.Println("untracked", untrackedBugs.Cardinality())

	untrackedBugs.Each(func(url string) bool {
		err := addProjectItem("kubescape", bugTrackingBoard, url)
		if err != nil {
			panic(err)
		}
		fmt.Println("added", url)
		return false
	})
}
