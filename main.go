package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"time"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/google/go-github/v66/github"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
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

// gh project item-add 4 --owner kubescape --url https://github.com/monalisa/myproject/issues/23

func getProjectItems(owner, board string, limit int) (mapset.Set[string], error) {
	cmd := exec.Command("gh", "project", "item-list", board, "--owner", owner, "-L", strconv.Itoa(limit), "--format", "json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list project items: %w", err)
	}
	var items ItemListOutput
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("failed to unmarshal project items: %w", err)
	}
	indices := mapset.NewSet[string]()
	for _, item := range items.Items {
		indices.Add(item.Content.URL)
	}
	return indices, nil
}

func Example() {
	src := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: os.Getenv("GITHUB_TOKEN")},
	)
	httpClient := oauth2.NewClient(context.Background(), src)

	client := githubv4.NewClient(httpClient)

	var q struct {
		Viewer struct {
			Login     string
			CreatedAt time.Time
			AvatarURL string `graphql:"avatarUrl(size: 72)"`
		}
	}
	err := client.Query(context.Background(), &q, nil)
	if err != nil {
		log.Fatalln(err)
	}
	fmt.Println(q.Viewer.Login)
	fmt.Println(q.Viewer.CreatedAt)
	fmt.Println(q.Viewer.AvatarURL)
}

func getIssues(ghClient *github.Client, owner, repo string) ([]*github.Issue, error) {
	opts := &github.IssueListByRepoOptions{
		State: "open",
	}
	var allIssues []*github.Issue
	for {
		issues, resp, err := ghClient.Issues.ListByRepo(context.Background(), owner, repo, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to list issues: %w", err)
		}
		for _, issue := range issues {
			if issue.PullRequestLinks != nil {
				continue
			}
			allIssues = append(allIssues, issue)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return allIssues, nil
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

func getRepositories(ghClient *github.Client, org string) ([]*github.Repository, error) {
	opts := &github.RepositoryListByOrgOptions{}
	var allRepos []*github.Repository
	for {
		repos, resp, err := ghClient.Repositories.ListByOrg(context.Background(), org, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to list repositories: %w", err)
		}
		allRepos = append(allRepos, repos...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return allRepos, nil
}

func main() {
	//ghClient := github.NewClient(nil)
	//repositories, err := getRepositories(ghClient, "kubescape")
	//if err != nil {
	//	panic(err)
	//}
	//allIssues := mapset.NewSet[int]()
	//for _, repo := range repositories {
	//	issues, err := getIssues(ghClient, "kubescape", *repo.Name)
	//	if err != nil {
	//		panic(err)
	//	}
	//	for _, issue := range issues {
	//		allIssues.Add(issue.GetNumber())
	//	}
	//}
	//fmt.Println("issues", allIssues)
	trackedBugs, err := getProjectItems("kubescape", bugTrackingBoard, 10)
	if err != nil {
		panic(err)
	}
	fmt.Println("tracked", trackedBugs)
	//fmt.Println("untracked", allIssues.Difference(trackedBugs))
	//ghClient.Projects.ListProjectCards(context.Background(), 1, nil)
}
