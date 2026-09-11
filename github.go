package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

const commentHistoryLimit = 30

// actor selects the Actor interface's login plus its concrete type. A deleted
// account yields a null author, hence the zero value and Typename == "".
type actor struct {
	Login    string
	Typename string `graphql:"__typename"`
}

// isHuman reports whether the actor is a real user account. Anything else -
// Bot, Organization, Mannequin, EnterpriseUserAccount, or a null author - is not.
func (a actor) isHuman() bool { return a.Typename == "User" }

type GHClient interface {
	AddProjectItem(ctx context.Context, owner, board, url string) error
	GetProjectID(ctx context.Context, owner, board string) (string, error)
	GetContentID(ctx context.Context, url string) (string, error)
	AddProjectItemWithIDs(ctx context.Context, projectID, contentID string) (string, error)
	GetProjectItemsWithState(ctx context.Context, owner, board string, limit int) ([]ProjectItem, error)
	GetBothProjectItems(ctx context.Context, owner, bugBoard, prBoard string, limit int) (mapset.Set[string], mapset.Set[string], error)
	GetIssuesAndPulls(ctx context.Context, repo string, limit int) ([]string, []PullRequestDetail, error)
	GetRepositories(ctx context.Context, owner string, limit int) ([]string, error)
	GetToArchiveFieldOption(ctx context.Context, owner, board string) (fieldID, optionID string, err error)
	GetSingleSelectOptions(ctx context.Context, owner, board string) (map[string]FieldOption, error)
	UpdateProjectItemField(ctx context.Context, projectID, itemID, fieldID, optionID string) error
}

// FieldOption identifies one option of a single-select project field.
type FieldOption struct {
	FieldID   string
	OptionID  string
	FieldName string
}

type ProjectItem struct {
	ID         string
	URL        string
	Closed     bool
	InArchive  bool
	StatusName string // current single-select status value, "" when unset
}

// singleSelectValue is one ProjectV2ItemFieldSingleSelectValue on an item.
type singleSelectValue struct {
	FieldName  string // owning field's name; "" when the fragment yields nothing
	OptionName string // the selected option's name
}

// resolveStatusName picks the item's current status from its single-select values.
func resolveStatusName(values []singleSelectValue) string {
	for _, v := range values {
		if v.FieldName == statusFieldName {
			return v.OptionName
		}
	}
	// Fall back to the field-name-agnostic matching the archive rule already uses.
	for _, v := range values {
		if slices.Contains(managedStatuses, v.OptionName) {
			return v.OptionName
		}
	}
	return ""
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
	variables := map[string]any{
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
	itemType := parts[3] // "issues" or "pull"
	number := parts[4]

	if itemType == "issues" {
		var query struct {
			Repository struct {
				Issue struct {
					ID string
				} `graphql:"issue(number: $number)"`
			} `graphql:"repository(owner: $owner, name: $repo)"`
		}
		variables := map[string]any{
			"owner":  githubv4.String(owner),
			"repo":   githubv4.String(repo),
			"number": githubv4.Int(parsePullNumber(number)),
		}
		err = withRetry(func() error { return c.v4Client.Query(ctx, &query, variables) })
		if err != nil {
			return "", err
		}
		return query.Repository.Issue.ID, nil
	}

	var query struct {
		Repository struct {
			PullRequest struct {
				ID string
			} `graphql:"pullRequest(number: $number)"`
		} `graphql:"repository(owner: $owner, name: $repo)"`
	}

	variables := map[string]any{
		"owner":  githubv4.String(owner),
		"repo":   githubv4.String(repo),
		"number": githubv4.Int(parsePullNumber(number)),
	}

	err = withRetry(func() error { return c.v4Client.Query(ctx, &query, variables) })
	if err != nil {
		return "", err
	}
	return query.Repository.PullRequest.ID, nil
}

func parsePullNumber(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "temporary conflict") || strings.Contains(s, "Something went wrong")
}

func withRetry(fn func() error) error {
	const maxRetries = 5
	backoff := 500 * time.Millisecond
	for i := range maxRetries {
		err := fn()
		if err == nil {
			return nil
		}
		if i < maxRetries-1 && isTransientError(err) {
			time.Sleep(backoff)
			backoff *= 2
			continue
		}
		return err
	}
	return nil
}

func (c *RealGHClient) AddProjectItemWithIDs(ctx context.Context, projectID, contentID string) (string, error) {
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

	err := withRetry(func() error { return c.v4Client.Mutate(ctx, &mutation, input, nil) })
	if err != nil {
		return "", err
	}
	return mutation.AddProjectV2ItemById.Item.ID, nil
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

	_, err = c.AddProjectItemWithIDs(ctx, projectID, contentID)
	return err
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
	variables := map[string]any{
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
	urls := mapset.NewSet[string]()
	var cursor *githubv4.String

	for {
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
						PageInfo struct {
							HasNextPage bool
							EndCursor   githubv4.String
						}
					} `graphql:"items(first: $limit, after: $cursor)"`
				} `graphql:"projectV2(number: $board)"`
			} `graphql:"organization(login: $owner)"`
		}

		boardNum, _ := strconv.Atoi(board)
		variables := map[string]any{
			"owner":  githubv4.String(owner),
			"board":  githubv4.Int(boardNum),
			"limit":  githubv4.Int(limit),
			"cursor": cursor,
		}

		err := c.v4Client.Query(ctx, &query, variables)
		if err != nil {
			return nil, err
		}

		for _, item := range query.Organization.ProjectV2.Items.Nodes {
			if url := item.Content.Issue.URL; url != "" {
				urls.Add(url)
			} else if url := item.Content.PullRequest.URL; url != "" {
				urls.Add(url)
			}
		}

		if !query.Organization.ProjectV2.Items.PageInfo.HasNextPage {
			break
		}
		cursor = new(query.Organization.ProjectV2.Items.PageInfo.EndCursor)
	}

	return urls, nil
}

func (c *RealGHClient) GetProjectItemsWithState(ctx context.Context, owner, board string, limit int) ([]ProjectItem, error) {
	var items []ProjectItem
	seenIDs := make(map[string]bool)

	filterQueries := []string{
		fmt.Sprintf("-status:%q", statusToArchive),
		fmt.Sprintf("status:%q is:open", statusToArchive),
	}

	for _, filterQuery := range filterQueries {
		var cursor *githubv4.String
		for {
			var query struct {
				Organization struct {
					ProjectV2 struct {
						Items struct {
							Nodes []struct {
								ID      string
								Content struct {
									Issue struct {
										URL   string
										State string
									} `graphql:"... on Issue"`
									PullRequest struct {
										URL   string
										State string
									} `graphql:"... on PullRequest"`
								}
								FieldValues struct {
									Nodes []struct {
										SingleSelectValue struct {
											Name  string
											Field struct {
												SingleSelectField struct {
													Name string
												} `graphql:"... on ProjectV2SingleSelectField"`
											}
										} `graphql:"... on ProjectV2ItemFieldSingleSelectValue"`
									}
								} `graphql:"fieldValues(first: 20)"`
							}
							PageInfo struct {
								HasNextPage bool
								EndCursor   githubv4.String
							}
						} `graphql:"items(first: $limit, after: $cursor, query: $filterQuery)"`
					} `graphql:"projectV2(number: $board)"`
				} `graphql:"organization(login: $owner)"`
			}

			boardNum, _ := strconv.Atoi(board)
			variables := map[string]any{
				"owner":       githubv4.String(owner),
				"board":       githubv4.Int(boardNum),
				"limit":       githubv4.Int(limit),
				"cursor":      cursor,
				"filterQuery": githubv4.String(filterQuery),
			}

			err := c.v4Client.Query(ctx, &query, variables)
			if err != nil {
				return nil, err
			}

			for _, node := range query.Organization.ProjectV2.Items.Nodes {
				if seenIDs[node.ID] {
					continue
				}
				seenIDs[node.ID] = true

				// githubv4 merges overlapping inline fragment fields, so Issue and
				// PullRequest structs both receive the same URL and state values.
				// Use Issue fields (always populated) and check both CLOSED and MERGED
				// so that merged PRs are correctly treated as closed.
				url := node.Content.Issue.URL
				if url == "" {
					url = node.Content.PullRequest.URL
				}
				if url == "" {
					continue
				}
				state := node.Content.Issue.State
				if state == "" {
					state = node.Content.PullRequest.State
				}
				inArchive := false
				for _, fv := range node.FieldValues.Nodes {
					if fv.SingleSelectValue.Name == statusToArchive {
						inArchive = true
						break
					}
				}
				var values []singleSelectValue
				for _, fv := range node.FieldValues.Nodes {
					if fv.SingleSelectValue.Name == "" {
						continue
					}
					values = append(values, singleSelectValue{
						FieldName:  fv.SingleSelectValue.Field.SingleSelectField.Name,
						OptionName: fv.SingleSelectValue.Name,
					})
				}
				items = append(items, ProjectItem{
					ID:         node.ID,
					URL:        url,
					Closed:     state == "CLOSED" || state == "MERGED",
					InArchive:  inArchive,
					StatusName: resolveStatusName(values),
				})
			}

			if !query.Organization.ProjectV2.Items.PageInfo.HasNextPage {
				break
			}
			cursor = new(query.Organization.ProjectV2.Items.PageInfo.EndCursor)
		}
	}

	return items, nil
}

func (c *RealGHClient) GetSingleSelectOptions(ctx context.Context, owner, board string) (map[string]FieldOption, error) {
	var query struct {
		Organization struct {
			ProjectV2 struct {
				Fields struct {
					Nodes []struct {
						SingleSelectField struct {
							ID      string
							Name    string
							Options []struct {
								ID   string
								Name string
							}
						} `graphql:"... on ProjectV2SingleSelectField"`
					}
				} `graphql:"fields(first: 50)"`
			} `graphql:"projectV2(number: $board)"`
		} `graphql:"organization(login: $owner)"`
	}

	boardNum, _ := strconv.Atoi(board)
	variables := map[string]any{
		"owner": githubv4.String(owner),
		"board": githubv4.Int(boardNum),
	}

	err := c.v4Client.Query(ctx, &query, variables)
	if err != nil {
		return nil, err
	}

	options := make(map[string]FieldOption)
	for _, node := range query.Organization.ProjectV2.Fields.Nodes {
		for _, opt := range node.SingleSelectField.Options {
			// First occurrence wins, preserving the original first-match semantics.
			if existing, dup := options[opt.Name]; dup {
				fmt.Printf("duplicate single-select option %q in board %s, keeping the one on field %q\n", opt.Name, board, existing.FieldName)
				continue
			}
			options[opt.Name] = FieldOption{
				FieldID:   node.SingleSelectField.ID,
				OptionID:  opt.ID,
				FieldName: node.SingleSelectField.Name,
			}
		}
	}
	return options, nil
}

func (c *RealGHClient) GetToArchiveFieldOption(ctx context.Context, owner, board string) (string, string, error) {
	opts, err := c.GetSingleSelectOptions(ctx, owner, board)
	if err != nil {
		return "", "", err
	}
	if o, ok := opts[statusToArchive]; ok {
		return o.FieldID, o.OptionID, nil
	}
	return "", "", fmt.Errorf("'To Archive' option not found in project board %s", board)
}

func (c *RealGHClient) UpdateProjectItemField(ctx context.Context, projectID, itemID, fieldID, optionID string) error {
	var mutation struct {
		UpdateProjectV2ItemFieldValue struct {
			ClientMutationID string
		} `graphql:"updateProjectV2ItemFieldValue(input: $input)"`
	}

	type ProjectV2FieldValueInput struct {
		SingleSelectOptionID string `json:"singleSelectOptionId"`
	}
	type UpdateProjectV2ItemFieldValueInput struct {
		ProjectID githubv4.ID              `json:"projectId"`
		ItemID    githubv4.ID              `json:"itemId"`
		FieldID   githubv4.ID              `json:"fieldId"`
		Value     ProjectV2FieldValueInput `json:"value"`
	}

	input := UpdateProjectV2ItemFieldValueInput{
		ProjectID: githubv4.ID(projectID),
		ItemID:    githubv4.ID(itemID),
		FieldID:   githubv4.ID(fieldID),
		Value:     ProjectV2FieldValueInput{SingleSelectOptionID: optionID},
	}

	return withRetry(func() error { return c.v4Client.Mutate(ctx, &mutation, input, nil) })
}

func (c *RealGHClient) GetIssuesAndPulls(ctx context.Context, repo string, limit int) ([]string, []PullRequestDetail, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
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
					URL            string
					Title          string
					IsDraft        bool
					UpdatedAt      time.Time
					ReviewDecision string
					Mergeable      string
					Author         actor
					Commits        struct {
						Nodes []struct {
							Commit struct {
								CommittedDate     time.Time
								StatusCheckRollup struct {
									State string
								}
							}
						}
					} `graphql:"commits(last: 1)"`
					Comments struct {
						Nodes []struct {
							CreatedAt time.Time
							Author    actor
						}
					} `graphql:"comments(last: $commentLimit)"`
					Reviews struct {
						Nodes []struct {
							SubmittedAt time.Time
							State       string
							Author      actor
						}
					} `graphql:"reviews(last: $commentLimit)"`
				}
			} `graphql:"pullRequests(first: $limit, states: OPEN)"`
		} `graphql:"repository(owner: $owner, name: $name)"`
	}

	variables := map[string]any{
		"owner":        githubv4.String(owner),
		"name":         githubv4.String(name),
		"limit":        githubv4.Int(limit),
		"commentLimit": githubv4.Int(commentHistoryLimit),
	}

	err := c.v4Client.Query(ctx, &query, variables)
	if err != nil {
		return nil, nil, err
	}

	issues := make([]string, len(query.Repository.Issues.Nodes))
	for i, issue := range query.Repository.Issues.Nodes {
		issues[i] = issue.URL
	}

	pulls := make([]PullRequestDetail, len(query.Repository.PullRequests.Nodes))
	for i, pr := range query.Repository.PullRequests.Nodes {
		ciState := ""
		var lastCommitAt time.Time
		if len(pr.Commits.Nodes) > 0 {
			ciState = pr.Commits.Nodes[0].Commit.StatusCheckRollup.State
			lastCommitAt = pr.Commits.Nodes[0].Commit.CommittedDate
		}

		type timedEvent struct {
			at    time.Time
			event CommentEvent
		}
		timed := make([]timedEvent, 0, len(pr.Comments.Nodes)+len(pr.Reviews.Nodes))
		for _, c := range pr.Comments.Nodes {
			timed = append(timed, timedEvent{c.CreatedAt, CommentEvent{Login: c.Author.Login, IsBot: !c.Author.isHuman(), At: c.CreatedAt}})
		}
		for _, r := range pr.Reviews.Nodes {
			// PENDING reviews are unsubmitted drafts, invisible to everyone else,
			// and carry a null submittedAt.
			if r.State == "PENDING" {
				continue
			}
			timed = append(timed, timedEvent{r.SubmittedAt, CommentEvent{Login: r.Author.Login, IsBot: !r.Author.isHuman(), At: r.SubmittedAt, State: r.State}})
		}
		// Stable: a review and a comment can share a timestamp, and an unstable
		// sort could flip which one is "last speaker" between runs.
		slices.SortStableFunc(timed, func(a, b timedEvent) int { return a.at.Compare(b.at) })

		conversation := make([]CommentEvent, len(timed))
		for j, t := range timed {
			conversation[j] = t.event
		}

		pulls[i] = PullRequestDetail{
			URL:            pr.URL,
			Title:          pr.Title,
			Repository:     repo,
			Author:         pr.Author.Login,
			IsDraft:        pr.IsDraft,
			UpdatedAt:      pr.UpdatedAt,
			ReviewDecision: pr.ReviewDecision,
			CIState:        ciState,
			Conversation:   conversation,
			Mergeable:      pr.Mergeable,
			LastCommitAt:   lastCommitAt,
		}
	}
	return issues, pulls, nil
}

func (c *RealGHClient) GetIssues(ctx context.Context, repo string, limit int) ([]string, error) {
	issues, _, err := c.GetIssuesAndPulls(ctx, repo, limit)
	return issues, err
}

func (c *RealGHClient) GetPulls(ctx context.Context, repo string, limit int) ([]PullRequestDetail, error) {
	_, pulls, err := c.GetIssuesAndPulls(ctx, repo, limit)
	return pulls, err
}

func (c *RealGHClient) GetRepositories(ctx context.Context, owner string, limit int) ([]string, error) {
	var query struct {
		Organization struct {
			Repositories struct {
				Nodes []struct {
					Name   string
					Issues struct {
						TotalCount int
					} `graphql:"issues(states: OPEN)"`
					PullRequests struct {
						TotalCount int
					} `graphql:"pullRequests(states: OPEN)"`
				}
			} `graphql:"repositories(first: $limit, privacy: PUBLIC, isArchived: false)"`
		} `graphql:"organization(login: $owner)"`
	}

	variables := map[string]any{
		"owner": githubv4.String(owner),
		"limit": githubv4.Int(limit),
	}

	err := c.v4Client.Query(ctx, &query, variables)
	if err != nil {
		return nil, err
	}

	var names []string
	for _, repo := range query.Organization.Repositories.Nodes {
		if repo.Issues.TotalCount == 0 && repo.PullRequests.TotalCount == 0 {
			continue
		}
		names = append(names, owner+"/"+repo.Name)
	}
	return names, nil
}
