package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/gammazero/workerpool"
	"github.com/joho/godotenv"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

const (
	bugTrackingBoard = "4"
	prTrackingBoard  = "5"
)

type GHClient interface {
	AddProjectItem(ctx context.Context, owner, board, url string) error
	GetProjectItems(ctx context.Context, owner, board string, limit int) (mapset.Set[string], error)
	GetIssues(ctx context.Context, repo string, limit int) ([]string, error)
	GetPulls(ctx context.Context, repo string, limit int) ([]string, error)
	GetRepositories(ctx context.Context, owner string, limit int) ([]string, error)
}

type RealGHClient struct {
	v4Client *githubv4.Client
}

func NewRealGHClient(token string) *RealGHClient {
	src := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	httpClient := oauth2.NewClient(context.Background(), src)
	return &RealGHClient{
		v4Client: githubv4.NewClient(httpClient),
	}
}

func (c *RealGHClient) AddProjectItem(ctx context.Context, owner, board, url string) error {
	// ProjectV2 items are better added via GraphQL, but for simplicity we can still shell out or implement Mutation
	// Given the goal of Option 1, I'll keep the shell-out for this specific one if it's complex,
	// but I'll implement the others via GraphQL.
	cmd := exec.CommandContext(ctx, "gh", "project", "item-add", board, "--owner", owner, "--url", url)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add project item: %s", out)
	}
	return nil
}

func (c *RealGHClient) GetProjectItems(ctx context.Context, owner, board string, limit int) (mapset.Set[string], error) {
	var query struct {
		Organization struct {
			ProjectV2 struct {
				Items struct {
					Nodes []struct {
						Content struct {
							Issue struct {
								URL string
							} `graphql:"... on Issue"`
							PullRequest struct {
								URL string
							} `graphql:"... on PullRequest"`
						}
					}
				} `graphql:"items(first: $limit)"`
			} `graphql:"projectV2(number: $board)"`
		} `graphql:"organization(login: $owner)"`
	}

	boardNum, _ := strconv.Atoi(board)
	variables := map[string]interface{}{
		"owner": githubv4.String(owner),
		"board": githubv4.Int(boardNum),
		"limit": githubv4.Int(limit),
	}

	err := c.v4Client.Query(ctx, &query, variables)
	if err != nil {
		return nil, err
	}

	urls := mapset.NewSet[string]()
	for _, item := range query.Organization.ProjectV2.Items.Nodes {
		if item.Content.Issue.URL != "" {
			urls.Add(item.Content.Issue.URL)
		} else if item.Content.PullRequest.URL != "" {
			urls.Add(item.Content.PullRequest.URL)
		}
	}
	return urls, nil
}

func (c *RealGHClient) GetIssues(ctx context.Context, repo string, limit int) ([]string, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo format: %s", repo)
	}

	var query struct {
		Repository struct {
			Issues struct {
				Nodes []struct {
					URL string
				}
			} `graphql:"issues(first: $limit, states: OPEN)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]interface{}{
		"owner": githubv4.String(parts[0]),
		"name":  githubv4.String(parts[1]),
		"limit": githubv4.Int(limit),
	}

	err := c.v4Client.Query(ctx, &query, variables)
	if err != nil {
		return nil, err
	}

	var urls []string
	for _, issue := range query.Repository.Issues.Nodes {
		urls = append(urls, issue.URL)
	}
	return urls, nil
}

func (c *RealGHClient) GetPulls(ctx context.Context, repo string, limit int) ([]string, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid repo format: %s", repo)
	}

	var query struct {
		Repository struct {
			PullRequests struct {
				Nodes []struct {
					URL string
				}
			} `graphql:"pullRequests(first: $limit, states: OPEN)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]interface{}{
		"owner": githubv4.String(parts[0]),
		"name":  githubv4.String(parts[1]),
		"limit": githubv4.Int(limit),
	}

	err := c.v4Client.Query(ctx, &query, variables)
	if err != nil {
		return nil, err
	}

	var urls []string
	for _, pr := range query.Repository.PullRequests.Nodes {
		urls = append(urls, pr.URL)
	}
	return urls, nil
}

func (c *RealGHClient) GetRepositories(ctx context.Context, owner string, limit int) ([]string, error) {
	var query struct {
		Organization struct {
			Repositories struct {
				Nodes []struct {
					Name string
				}
			} `graphql:"repositories(first: $limit, privacy: PUBLIC, isArchived: false)"`
		} `graphql:"organization(login: $owner)"`
	}

	variables := map[string]interface{}{
		"owner": githubv4.String(owner),
		"limit": githubv4.Int(limit),
	}

	err := c.v4Client.Query(ctx, &query, variables)
	if err != nil {
		return nil, err
	}

	var names []string
	for _, repo := range query.Organization.Repositories.Nodes {
		names = append(names, owner+"/"+repo.Name)
	}
	return names, nil
}

func main() {
	_ = godotenv.Load()
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		fmt.Println("GITHUB_TOKEN env var is required for GraphQL API")
		os.Exit(1)
	}
	client := NewRealGHClient(token)
	Run(context.Background(), client)
}

func Run(ctx context.Context, client GHClient) {
	limit := 100
	repositories, err := client.GetRepositories(ctx, "kubescape", limit)
	if err != nil {
		panic(err)
	}
	allIssues := mapset.NewSet[string]()
	allPulls := mapset.NewSet[string]()
	var mu sync.Mutex
	wp := workerpool.New(runtime.GOMAXPROCS(0))
	for _, repo := range repositories {
		wp.Submit(func() {
			fmt.Println("processing", repo)
			var wg sync.WaitGroup
			var issues, pulls []string
			var errI, errP error
			wg.Add(2)
			go func() {
				defer wg.Done()
				issues, errI = client.GetIssues(ctx, repo, limit)
			}()
			go func() {
				defer wg.Done()
				pulls, errP = client.GetPulls(ctx, repo, limit)
			}()
			wg.Wait()
			if errI != nil {
				panic(errI)
			}
			if errP != nil {
				panic(errP)
			}
			mu.Lock()
			allIssues.Append(issues...)
			allPulls.Append(pulls...)
			mu.Unlock()
		})
	}
	wp.StopWait()
	fmt.Println("issues", allIssues.Cardinality())
	fmt.Println("pulls", allPulls.Cardinality())

	trackedIssues, err := client.GetProjectItems(ctx, "kubescape", bugTrackingBoard, limit)
	if err != nil {
		panic(err)
	}
	fmt.Println("tracked issues", trackedIssues.Cardinality())

	trackedPulls, err := client.GetProjectItems(ctx, "kubescape", prTrackingBoard, limit)
	if err != nil {
		panic(err)
	}
	fmt.Println("tracked pulls", trackedPulls.Cardinality())

	untrackedIssues := allIssues.Difference(trackedIssues)
	fmt.Println("untracked issues", untrackedIssues.Cardinality())

	wp = workerpool.New(runtime.GOMAXPROCS(0))
	untrackedIssues.Each(func(url string) bool {
		wp.Submit(func() {
			err := client.AddProjectItem(ctx, "kubescape", bugTrackingBoard, url)
			if err != nil {
				panic(err)
			}
			fmt.Println("added issue", url)
		})
		return false
	})

	untrackedPulls := allPulls.Difference(trackedPulls)
	fmt.Println("untracked pulls", untrackedPulls.Cardinality())

	untrackedPulls.Each(func(url string) bool {
		wp.Submit(func() {
			err := client.AddProjectItem(ctx, "kubescape", prTrackingBoard, url)
			if err != nil {
				panic(err)
			}
			fmt.Println("added pr", url)
		})
		return false
	})
	wp.StopWait()
}
