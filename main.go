package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

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
	GetProjectID(ctx context.Context, owner, board string) (string, error)
	GetContentID(ctx context.Context, url string) (string, error)
	AddProjectItemWithIDs(ctx context.Context, projectID, contentID string) error
	GetProjectItems(ctx context.Context, owner, board string, limit int) (mapset.Set[string], error)
	GetBothProjectItems(ctx context.Context, owner, bugBoard, prBoard string, limit int) (mapset.Set[string], mapset.Set[string], error)
	GetIssuesAndPulls(ctx context.Context, repo string, limit int) ([]string, []string, error)
	GetRepositories(ctx context.Context, owner string, limit int) ([]string, error)
}

type RealGHClient struct {
	v4Client *githubv4.Client
}

func NewRealGHClient(token string) *RealGHClient {
	src := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	transport := &oauth2.Transport{
		Source: src,
		Base: &http.Transport{
			MaxIdleConns:          200,
			MaxIdleConnsPerHost:   100,
			IdleConnTimeout:       30 * time.Second,
			MaxConnsPerHost:       200,
			ResponseHeaderTimeout: 60 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
		},
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   120 * time.Second,
	}
	return &RealGHClient{
		v4Client: githubv4.NewClient(httpClient),
	}
}

func (c *RealGHClient) GetProjectID(ctx context.Context, owner, board string) (string, error) {
	var query struct {
		Organization struct {
			ProjectV2 struct {
				ID string
			} `graphql:"projectV2(number: $board)"`
		} `graphql:"organization(login: $owner)"`
	}

	boardNum, _ := strconv.Atoi(board)
	variables := map[string]interface{}{
		"owner": githubv4.String(owner),
		"board": githubv4.Int(boardNum),
	}

	err := c.v4Client.Query(ctx, &query, variables)
	if err != nil {
		return "", err
	}
	return query.Organization.ProjectV2.ID, nil
}

func (c *RealGHClient) GetContentID(ctx context.Context, urlStr string) (string, error) {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return "", err
	}

	parts := strings.Split(parsedURL.Path, "/")
	if len(parts) < 5 {
		return "", fmt.Errorf("invalid GitHub URL format")
	}

	owner := parts[1]
	repo := parts[2]
	pullNum := parts[4]

	var query struct {
		Repository struct {
			PullRequest struct {
				ID string
			} `graphql:"pullRequest(number: $number)"`
		} `graphql:"repository(owner: $owner, name: $repo)"`
	}

	variables := map[string]interface{}{
		"owner":  githubv4.String(owner),
		"repo":   githubv4.String(repo),
		"number": githubv4.Int(parsePullNumber(pullNum)),
	}

	err = c.v4Client.Query(ctx, &query, variables)
	if err != nil {
		return "", err
	}
	return query.Repository.PullRequest.ID, nil
}

func parsePullNumber(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func (c *RealGHClient) AddProjectItemWithIDs(ctx context.Context, projectID, contentID string) error {
	var mutation struct {
		AddProjectV2ItemById struct {
			ClientMutationID string
			Item             struct {
				ID string
			}
		} `graphql:"addProjectV2ItemById(input: $input)"`
	}

	type AddProjectV2ItemByIdInput struct {
		ProjectID githubv4.ID `json:"projectId" graphql:"projectId"`
		ContentID githubv4.ID `json:"contentId" graphql:"contentId"`
	}

	input := AddProjectV2ItemByIdInput{
		ProjectID: githubv4.ID(projectID),
		ContentID: githubv4.ID(contentID),
	}

	const maxRetries = 5
	backoff := 500 * time.Millisecond
	for i := range maxRetries {
		err := c.v4Client.Mutate(ctx, &mutation, input, nil)
		if err == nil {
			return nil
		}
		if i < maxRetries-1 && strings.Contains(err.Error(), "temporary conflict") {
			time.Sleep(backoff)
			backoff *= 2
			continue
		}
		return err
	}
	return nil
}

func (c *RealGHClient) AddProjectItem(ctx context.Context, owner, board, url string) error {
	projectID, err := c.GetProjectID(ctx, owner, board)
	if err != nil {
		return fmt.Errorf("failed to get project ID: %w", err)
	}

	contentID, err := c.GetContentID(ctx, url)
	if err != nil {
		return fmt.Errorf("failed to get content ID: %w", err)
	}

	return c.AddProjectItemWithIDs(ctx, projectID, contentID)
}

func (c *RealGHClient) GetBothProjectItems(ctx context.Context, owner, bugBoard, prBoard string, limit int) (mapset.Set[string], mapset.Set[string], error) {
	var query struct {
		Organization struct {
			BugProject struct {
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
			} `graphql:"projectV2(number: $bugBoard)"`
			PRProject struct {
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
			} `graphql:"projectV2(number: $prBoard)"`
		} `graphql:"organization(login: $owner)"`
	}

	bugBoardNum, _ := strconv.Atoi(bugBoard)
	prBoardNum, _ := strconv.Atoi(prBoard)
	variables := map[string]interface{}{
		"owner":    githubv4.String(owner),
		"bugBoard": githubv4.Int(bugBoardNum),
		"prBoard":  githubv4.Int(prBoardNum),
		"limit":    githubv4.Int(limit),
	}

	err := c.v4Client.Query(ctx, &query, variables)
	if err != nil {
		return nil, nil, err
	}

	bugURLs := mapset.NewSet[string]()
	for _, item := range query.Organization.BugProject.Items.Nodes {
		if url := item.Content.Issue.URL; url != "" {
			bugURLs.Add(url)
		} else if url := item.Content.PullRequest.URL; url != "" {
			bugURLs.Add(url)
		}
	}

	prURLs := mapset.NewSet[string]()
	for _, item := range query.Organization.PRProject.Items.Nodes {
		if url := item.Content.Issue.URL; url != "" {
			prURLs.Add(url)
		} else if url := item.Content.PullRequest.URL; url != "" {
			prURLs.Add(url)
		}
	}
	return bugURLs, prURLs, nil
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
		if url := item.Content.Issue.URL; url != "" {
			urls.Add(url)
		} else if url := item.Content.PullRequest.URL; url != "" {
			urls.Add(url)
		}
	}
	return urls, nil
}

func (c *RealGHClient) GetIssuesAndPulls(ctx context.Context, repo string, limit int) ([]string, []string, error) {
	slashIdx := strings.IndexByte(repo, '/')
	if slashIdx < 0 {
		return nil, nil, fmt.Errorf("invalid repo format: %s", repo)
	}

	var query struct {
		Repository struct {
			Issues struct {
				Nodes []struct {
					URL string
				}
			} `graphql:"issues(first: $limit, states: OPEN)"`
			PullRequests struct {
				Nodes []struct {
					URL string
				}
			} `graphql:"pullRequests(first: $limit, states: OPEN)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]interface{}{
		"owner": githubv4.String(repo[:slashIdx]),
		"name":  githubv4.String(repo[slashIdx+1:]),
		"limit": githubv4.Int(limit),
	}

	err := c.v4Client.Query(ctx, &query, variables)
	if err != nil {
		return nil, nil, err
	}

	issues := make([]string, len(query.Repository.Issues.Nodes))
	for i, issue := range query.Repository.Issues.Nodes {
		issues[i] = issue.URL
	}

	pulls := make([]string, len(query.Repository.PullRequests.Nodes))
	for i, pr := range query.Repository.PullRequests.Nodes {
		pulls[i] = pr.URL
	}
	return issues, pulls, nil
}

func (c *RealGHClient) GetIssues(ctx context.Context, repo string, limit int) ([]string, error) {
	issues, _, err := c.GetIssuesAndPulls(ctx, repo, limit)
	return issues, err
}

func (c *RealGHClient) GetPulls(ctx context.Context, repo string, limit int) ([]string, error) {
	_, pulls, err := c.GetIssuesAndPulls(ctx, repo, limit)
	return pulls, err
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
	start := time.Now()
	defer func() {
		fmt.Printf("Total time: %v\n", time.Since(start))
	}()

	limit := 100
	repositories, err := client.GetRepositories(ctx, "kubescape", limit)
	if err != nil {
		panic(err)
	}

	issueChan := make(chan []string, max(runtime.GOMAXPROCS(0), len(repositories)))
	pullChan := make(chan []string, max(runtime.GOMAXPROCS(0), len(repositories)))
	wp := workerpool.New(runtime.GOMAXPROCS(0) * 4)

	for _, repo := range repositories {
		repo := repo
		wp.Submit(func() {
			issues, pulls, err := client.GetIssuesAndPulls(ctx, repo, limit)
			if err != nil {
				fmt.Println("error processing repo:", repo, err)
				return
			}
			if len(issues) > 0 {
				issueChan <- issues
			}
			if len(pulls) > 0 {
				pullChan <- pulls
			}
		})
	}
	wp.StopWait()
	close(issueChan)
	close(pullChan)

	allIssues := mapset.NewSet[string]()
	allPulls := mapset.NewSet[string]()

	for issues := range issueChan {
		allIssues.Append(issues...)
	}
	for pulls := range pullChan {
		allPulls.Append(pulls...)
	}

	fmt.Println("issues", allIssues.Cardinality())
	fmt.Println("pulls", allPulls.Cardinality())

	var trackedIssues, trackedPulls mapset.Set[string]
	var errI, errP error
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		trackedIssues, errI = client.GetProjectItems(ctx, "kubescape", bugTrackingBoard, limit)
	}()

	go func() {
		defer wg.Done()
		trackedPulls, errP = client.GetProjectItems(ctx, "kubescape", prTrackingBoard, limit)
	}()

	wg.Wait()

	if errI != nil {
		panic(errI)
	}
	if errP != nil {
		panic(errP)
	}

	fmt.Println("tracked issues", trackedIssues.Cardinality())
	fmt.Println("tracked pulls", trackedPulls.Cardinality())

	untrackedIssues := allIssues.Difference(trackedIssues)
	untrackedPulls := allPulls.Difference(trackedPulls)
	fmt.Println("untracked issues", untrackedIssues.Cardinality())
	fmt.Println("untracked pulls", untrackedPulls.Cardinality())

	issueURLs := make([]string, 0, untrackedIssues.Cardinality())
	untrackedIssues.Each(func(url string) bool {
		issueURLs = append(issueURLs, url)
		return false
	})

	prURLs := make([]string, 0, untrackedPulls.Cardinality())
	untrackedPulls.Each(func(url string) bool {
		prURLs = append(prURLs, url)
		return false
	})

	var bugProjectID, prProjectID string
	var errBug, errPR error
	wg.Add(2)
	go func() {
		defer wg.Done()
		bugProjectID, errBug = client.GetProjectID(ctx, "kubescape", bugTrackingBoard)
	}()
	go func() {
		defer wg.Done()
		prProjectID, errPR = client.GetProjectID(ctx, "kubescape", prTrackingBoard)
	}()
	wg.Wait()
	if errBug != nil {
		panic(errBug)
	}
	if errPR != nil {
		panic(errPR)
	}

	wg.Add(2)

	go func() {
		defer wg.Done()
		wp := workerpool.New(runtime.GOMAXPROCS(0) * 4)
		for _, url := range issueURLs {
			url := url
			wp.Submit(func() {
				contentID, err := client.GetContentID(ctx, url)
				if err != nil {
					fmt.Println("error adding issue", url, err)
					return
				}
				err = client.AddProjectItemWithIDs(ctx, bugProjectID, contentID)
				if err != nil {
					fmt.Println("error adding issue", url, err)
				} else {
					fmt.Println("added issue", url)
				}
			})
		}
		wp.StopWait()
	}()

	go func() {
		defer wg.Done()
		wp := workerpool.New(runtime.GOMAXPROCS(0) * 4)
		for _, url := range prURLs {
			url := url
			wp.Submit(func() {
				contentID, err := client.GetContentID(ctx, url)
				if err != nil {
					fmt.Println("error adding pr", url, err)
					return
				}
				err = client.AddProjectItemWithIDs(ctx, prProjectID, contentID)
				if err != nil {
					fmt.Println("error adding pr", url, err)
				} else {
					fmt.Println("added pr", url)
				}
			})
		}
		wp.StopWait()
	}()

	wg.Wait()
}
