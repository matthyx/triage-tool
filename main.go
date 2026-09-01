package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"slices"
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
	bugTrackingBoard      = "4"
	prTrackingBoard       = "5"
	staleThresholdDays    = 7
	statusFieldName       = "Status"
	statusToArchive       = "To Archive"
	statusWaitingOnAuthor = "Waiting on Author"
	statusNeedsReviewer   = "Needs Reviewer"
	statusWIP             = "WIP"
	commentHistoryLimit   = 30

	// mergeableConflicting is the one MergeableState that means the author must
	// act. MERGEABLE, UNKNOWN and "" are treated as absence of signal.
	mergeableConflicting = "CONFLICTING"
	// reviewChangesRequested is both a Review.state and a reviewDecision value.
	reviewChangesRequested = "CHANGES_REQUESTED"
	// reviewApproved is a reviewDecision value.
	reviewApproved = "APPROVED"

	// reason* are the routing reasons routeTarget can return. They exist so the
	// printed line and the counter switch cannot drift apart by a typo.
	reasonMergeConflict    = "merge-conflict"
	reasonChangesRequested = "changes-requested"
	reasonLastCommenter    = "last-commenter"
	reasonOwnPR            = "own-pr"
)

var myTeam = []string{"matthyx", "entlein"}

// routableStatuses are the only current statuses the routing rule may overwrite.
// "To Archive" is deliberately absent: a human can park an open PR there to mean
// "stop showing me this", and routing must not yank it back out.
var routableStatuses = []string{"", statusWaitingOnAuthor, statusNeedsReviewer, statusWIP}

// CommentEvent is one conversation event, already stripped of GraphQL shapes.
type CommentEvent struct {
	Login string
	IsBot bool
	At    time.Time // comment createdAt / review submittedAt
	State string    // review state ("" for issue comments)
}

// actor selects the Actor interface's login plus its concrete type. A deleted
// account yields a null author, hence the zero value and Typename == "".
type actor struct {
	Login    string
	Typename string `graphql:"__typename"`
}

// isHuman reports whether the actor is a real user account. Anything else -
// Bot, Organization, Mannequin, EnterpriseUserAccount, or a null author - is not.
func (a actor) isHuman() bool { return a.Typename == "User" }

type PullRequestDetail struct {
	URL            string
	Title          string
	Repository     string
	Author         string
	IsDraft        bool
	UpdatedAt      time.Time
	ReviewDecision string
	CIState        string
	Conversation   []CommentEvent // oldest first
	Mergeable      string         // MergeableState: "MERGEABLE" | "CONFLICTING" | "UNKNOWN" | ""
	LastCommitAt   time.Time      // committedDate of the PR tip commit; zero when absent
}

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

// managedStatuses are the status names this tool knows how to set.
var managedStatuses = []string{statusToArchive, statusWaitingOnAuthor, statusNeedsReviewer, statusWIP}

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

// botLogins are actors whose comments never count as a human reply. This is
// secondary to the __typename check and only catches human-typed service accounts.
var botLogins = map[string]struct{}{
	"dependabot": {}, "github-actions": {}, "codecov": {},
	"renovate": {}, "sonarcloud": {}, "copilot-pull-request-reviewer": {},
	"coderabbitai": {},
}

func isBotLogin(login string) bool {
	if _, ok := botLogins[login]; ok {
		return true
	}
	return strings.HasSuffix(login, "[bot]")
}

// classifyReviewStatus returns the target column name for a PR given its
// chronological (oldest first) conversation, or "" when no move applies.
func classifyReviewStatus(events []CommentEvent, team []string) string {
	var humans []CommentEvent
	for _, e := range events {
		if e.IsBot || isBotLogin(e.Login) {
			continue
		}
		humans = append(humans, e)
	}
	if len(humans) == 0 {
		return ""
	}
	if slices.Contains(team, humans[len(humans)-1].Login) {
		return statusWaitingOnAuthor
	}
	// a team member participated and someone else spoke last, so the team was replied to.
	if slices.ContainsFunc(humans, func(e CommentEvent) bool { return slices.Contains(team, e.Login) }) {
		return statusNeedsReviewer
	}
	return ""
}

// newestChangeRequestAt returns the time of the newest non-bot CHANGES_REQUESTED
// review in the conversation, and whether one was found. It is generic over the
// reviewer: any human reviewer's change request counts, not just team members', so
// this function never references myTeam. It scans the whole slice rather than
// assuming the caller sorted it.
func newestChangeRequestAt(events []CommentEvent) (time.Time, bool) {
	var newest time.Time
	found := false
	for _, e := range events {
		if e.IsBot || isBotLogin(e.Login) {
			continue
		}
		if e.State != reviewChangesRequested {
			continue
		}
		if !found || e.At.After(newest) {
			newest = e.At
			found = true
		}
	}
	return newest, found
}

// changesRequestedOutstanding reports whether the PR carries a change request that
// the author has not yet addressed, per fork D3: addressed means either a commit
// newer than the newest change request, or an event from the PR author after it.
func changesRequestedOutstanding(pr PullRequestDetail) bool {
	// GitHub's aggregate is the gate; nothing else can turn the pin on.
	if pr.ReviewDecision != reviewChangesRequested {
		return false
	}
	at, ok := newestChangeRequestAt(pr.Conversation)
	if !ok {
		// Window miss: reviewDecision says CHANGES_REQUESTED but no human change
		// request is visible in the fetched window (full window / bot-only change
		// request / all-dismissed). Pin conservatively.
		return true
	}
	// D1: addressed by a push. The tip commit counts as an author action
	// regardless of who actually committed it - a known-false simplification
	// (kubescape/node-agent#808 has a tip commit by the reviewer). A false
	// "addressed" only costs the new protection: routeTarget's P4 then returns
	// classifyReviewStatus's own unmodified answer. A commit-authorship filter
	// is a deliberate follow-up, not a v2 clause.
	if !pr.LastCommitAt.IsZero() && pr.LastCommitAt.After(at) {
		return false
	}
	// D2: addressed by a reply from the PR author.
	for _, e := range pr.Conversation {
		if !e.IsBot && !isBotLogin(e.Login) && e.Login == pr.Author && e.At.After(at) {
			return false
		}
	}
	return true
}

// routeTarget decides the column an open PR belongs in and why, applying the
// signal precedence: own PR to WIP first, then last-commenter as a gate, then
// merge conflict, then an outstanding change request. Both overrides can only
// redirect a move toward "Waiting on Author"; neither can create a move the
// last-commenter rule did not already produce. Returns ("", "") when no move
// applies.
func routeTarget(pr PullRequestDetail, team []string) (string, string) {
	if slices.Contains(team, pr.Author) {
		return statusWIP, reasonOwnPR
	}
	// P0: the frozen last-commenter rule.
	target := classifyReviewStatus(pr.Conversation, team)
	// P1: MONOTONICITY GATE. No signal may create a routing candidate the
	// last-commenter rule did not already produce. Everything below this line
	// depends on it; weakening it to make a test pass is never the right fix.
	if target == "" {
		return "", ""
	}
	// P2: a merge conflict is a machine-verifiable fact, so it outranks P3 and
	// owns the printed reason when both hold. UNKNOWN and "" are no signal.
	if pr.Mergeable == mergeableConflicting {
		return statusWaitingOnAuthor, reasonMergeConflict
	}
	// P3: an outstanding change request the author has not addressed.
	if changesRequestedOutstanding(pr) {
		return statusWaitingOnAuthor, reasonChangesRequested
	}
	// P4: the structural safety net. Whenever an override fails to fire for any
	// reason, the answer is classifyReviewStatus's own, unchanged.
	return target, reasonLastCommenter
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

		for _, node := range query.Organization.ProjectV2.Items.Nodes {
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
				if fv.SingleSelectValue.Name == "To Archive" {
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
					Name string
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
	pullChan := make(chan []PullRequestDetail, max(runtime.GOMAXPROCS(0), len(repositories)))
	wp := workerpool.New(runtime.GOMAXPROCS(0) * 4)

	for _, repo := range repositories {
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
	allPRDetails := make(map[string]PullRequestDetail)

	for issues := range issueChan {
		allIssues.Append(issues...)
	}
	for pulls := range pullChan {
		for _, pr := range pulls {
			allPulls.Add(pr.URL)
			allPRDetails[pr.URL] = pr
		}
	}

	fmt.Println("issues", allIssues.Cardinality())
	fmt.Println("pulls", allPulls.Cardinality())

	var bugItems, prItems []ProjectItem
	var errI, errP error
	var wg sync.WaitGroup
	wg.Go(func() {
		bugItems, errI = client.GetProjectItemsWithState(ctx, "kubescape", bugTrackingBoard, limit)
	})
	wg.Go(func() {
		prItems, errP = client.GetProjectItemsWithState(ctx, "kubescape", prTrackingBoard, limit)
	})
	wg.Wait()

	if errI != nil {
		panic(errI)
	}
	if errP != nil {
		panic(errP)
	}

	trackedIssues := mapset.NewSet[string]()
	for _, item := range bugItems {
		trackedIssues.Add(item.URL)
	}
	trackedPulls := mapset.NewSet[string]()
	for _, item := range prItems {
		trackedPulls.Add(item.URL)
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
	wg.Go(func() {
		bugProjectID, errBug = client.GetProjectID(ctx, "kubescape", bugTrackingBoard)
	})
	wg.Go(func() {
		prProjectID, errPR = client.GetProjectID(ctx, "kubescape", prTrackingBoard)
	})
	wg.Wait()
	if errBug != nil {
		panic(errBug)
	}
	if errPR != nil {
		panic(errPR)
	}

	var newPRItemsMu sync.Mutex
	var newPRItems []ProjectItem

	wg.Go(func() {
		wp := workerpool.New(runtime.GOMAXPROCS(0) * 4)
		for _, url := range issueURLs {
			wp.Submit(func() {
				contentID, err := client.GetContentID(ctx, url)
				if err != nil {
					fmt.Println("error adding issue", url, err)
					return
				}
				_, err = client.AddProjectItemWithIDs(ctx, bugProjectID, contentID)
				if err != nil {
					fmt.Println("error adding issue", url, err)
				} else {
					fmt.Println("added issue", url)
				}
			})
		}
		wp.StopWait()
	})
	wg.Go(func() {
		wp := workerpool.New(runtime.GOMAXPROCS(0) * 4)
		for _, url := range prURLs {
			wp.Submit(func() {
				contentID, err := client.GetContentID(ctx, url)
				if err != nil {
					fmt.Println("error adding pr", url, err)
					return
				}
				itemID, err := client.AddProjectItemWithIDs(ctx, prProjectID, contentID)
				if err != nil {
					fmt.Println("error adding pr", url, err)
				} else {
					fmt.Println("added pr", url)
					newPRItemsMu.Lock()
					newPRItems = append(newPRItems, ProjectItem{
						ID:         itemID,
						URL:        url,
						Closed:     false,
						InArchive:  false,
						StatusName: "",
					})
					newPRItemsMu.Unlock()

					pr := allPRDetails[url]
					if !slices.Contains(myTeam, pr.Author) {
						if repoName, prNumber, ok := parsePRRepoAndNumber(url); ok {
							if err := createMulticaIssue(ctx, repoName, prNumber, url); err != nil {
								fmt.Printf("error creating multica issue for pr %s: %v\n", url, err)
							} else {
								fmt.Printf("created multica issue for pr %s\n", url)
							}
						}
					}
				}
			})
		}
		wp.StopWait()
	})
	wg.Wait()

	prItems = append(prItems, newPRItems...)

	// Archive closed/merged items on both boards
	var bugFieldID, bugOptionID, prFieldID, prOptionID string
	wg.Go(func() {
		bugFieldID, bugOptionID, errBug = client.GetToArchiveFieldOption(ctx, "kubescape", bugTrackingBoard)
	})
	wg.Go(func() {
		prFieldID, prOptionID, errPR = client.GetToArchiveFieldOption(ctx, "kubescape", prTrackingBoard)
	})
	wg.Wait()
	if errBug != nil {
		panic(errBug)
	}
	if errPR != nil {
		panic(errPR)
	}

	wg.Go(func() {
		wp := workerpool.New(runtime.GOMAXPROCS(0) * 4)
		for _, item := range bugItems {
			if !item.Closed || item.InArchive {
				continue
			}
			wp.Submit(func() {
				err := client.UpdateProjectItemField(ctx, bugProjectID, item.ID, bugFieldID, bugOptionID)
				if err != nil {
					fmt.Println("error moving issue to archive", item.URL, err)
				} else {
					fmt.Println("moved issue to archive", item.URL)
				}
			})
		}
		wp.StopWait()
	})
	wg.Go(func() {
		wp := workerpool.New(runtime.GOMAXPROCS(0) * 4)
		for _, item := range prItems {
			if !item.Closed || item.InArchive {
				continue
			}
			wp.Submit(func() {
				err := client.UpdateProjectItemField(ctx, prProjectID, item.ID, prFieldID, prOptionID)
				if err != nil {
					fmt.Println("error moving pr to archive", item.URL, err)
				} else {
					fmt.Println("moved pr to archive", item.URL)
				}
			})
		}
		wp.StopWait()
	})
	wg.Wait()

	// Approved PRs are never routed (A8), so a column set before approval can go
	// Route open PRs between columns based on who spoke last in the conversation.
	// Disabled loudly rather than guessed at when the board is not shaped as expected.
	dryRun := os.Getenv("TRIAGE_ROUTING_DRY_RUN") != ""
	prOptions, err := client.GetSingleSelectOptions(ctx, "kubescape", prTrackingBoard)

	wa, okWA := prOptions[statusWaitingOnAuthor]
	nr, okNR := prOptions[statusNeedsReviewer]
	ta := prOptions[statusToArchive]
	wip, okWIP := prOptions[statusWIP]

	switch {
	case err != nil:
		fmt.Printf("routing disabled: %v\n", err)
	case !okWA || !okNR || !okWIP:
		fmt.Println(`routing disabled: board is missing "Waiting on Author", "Needs Reviewer", and/or "WIP"`)
	case wa.FieldID != nr.FieldID || wa.FieldID != ta.FieldID || wa.FieldID != wip.FieldID:
		fmt.Println("routing options span multiple fields, disabling routing")
	case wa.FieldName != statusFieldName:
		fmt.Printf("status field is not named %q, disabling routing\n", statusFieldName)
	default:
		var mu sync.Mutex
		var candidates, moved, alreadyInTarget, noSuchOption, skippedUnmanaged int
		var byConflict, byChangesRequested int
		loggedMissingOption := map[string]bool{}

		routeWP := workerpool.New(runtime.GOMAXPROCS(0) * 4)
		for _, item := range prItems {
			// prItems is stale in memory after the archive mutations, so closed
			// items are excluded explicitly rather than by block ordering.
			if item.Closed {
				continue
			}
			if !slices.Contains(routableStatuses, item.StatusName) {
				skippedUnmanaged++
				continue
			}
			pr, ok := allPRDetails[item.URL]
			if !ok {
				continue
			}
			target, reason := routeTarget(pr, myTeam)
			if target == "" {
				continue
			}
			candidates++
			// Counted here, above the disposition guards, so the counters mean
			// "decisions produced by a new signal" - including PRs already in the
			// right column. These increments run on the single-threaded loop
			// goroutine (before routeWP.Submit), so they need no mutex; only
			// moved is touched inside the worker closure under mu.
			switch reason {
			case reasonMergeConflict:
				byConflict++
			case reasonChangesRequested:
				byChangesRequested++
			}
			if item.StatusName == target {
				alreadyInTarget++
				continue
			}
			opt, ok := prOptions[target]
			if !ok {
				noSuchOption++
				if !loggedMissingOption[target] {
					loggedMissingOption[target] = true
					fmt.Printf("no %q option on the PR board\n", target)
				}
				continue
			}
			current := item.StatusName
			if current == "" {
				current = "(none)"
			}
			if dryRun {
				fmt.Printf("would move pr %s from %q to %q (%s)\n", item.URL, current, target, reason)
				continue
			}
			routeWP.Submit(func() {
				err := client.UpdateProjectItemField(ctx, prProjectID, item.ID, opt.FieldID, opt.OptionID)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					fmt.Printf("error moving pr %s to %q: %v\n", item.URL, target, err)
					return
				}
				moved++
				fmt.Printf("moved pr %s from %q to %q (%s)\n", item.URL, current, target, reason)
			})
		}
		routeWP.StopWait()
		fmt.Printf("routing: %d candidates, %d moved, %d already-in-target, %d no-such-option, %d skipped-unmanaged-status, %d by-conflict, %d by-changes-requested\n",
			candidates, moved, alreadyInTarget, noSuchOption, skippedUnmanaged, byConflict, byChangesRequested)
	}

	// Process and prioritize PRs needing attention
	var failingCIPRs []PullRequestDetail
	var approvedPRs []PullRequestDetail
	var staleOrWaitingPRs []PullRequestDetail

	now := time.Now()
	for _, pr := range allPRDetails {
		// Priority 1: Failing CI/CD
		if pr.CIState == "FAILURE" || pr.CIState == "ERROR" {
			failingCIPRs = append(failingCIPRs, pr)
			continue
		}
		// Priority 2: Approved & Ready to Merge
		if pr.ReviewDecision == reviewApproved && pr.Mergeable != mergeableConflicting {
			approvedPRs = append(approvedPRs, pr)
			continue
		}
		// Priority 3: Stale / Waiting on Reviews
		if pr.IsDraft {
			continue
		}
		isStale := now.Sub(pr.UpdatedAt) > staleThresholdDays*24*time.Hour
		isWaitingReview := pr.ReviewDecision == "REVIEW_REQUIRED"
		if isStale || isWaitingReview {
			staleOrWaitingPRs = append(staleOrWaitingPRs, pr)
		}
	}

	slices.SortFunc(failingCIPRs, func(a, b PullRequestDetail) int {
		return a.UpdatedAt.Compare(b.UpdatedAt)
	})
	slices.SortFunc(approvedPRs, func(a, b PullRequestDetail) int {
		return a.UpdatedAt.Compare(b.UpdatedAt)
	})
	slices.SortFunc(staleOrWaitingPRs, func(a, b PullRequestDetail) int {
		return a.UpdatedAt.Compare(b.UpdatedAt)
	})

	fmt.Println()
	fmt.Println("========================================================================")
	fmt.Println("                       PULL REQUESTS NEEDING ATTENTION                  ")
	fmt.Println("========================================================================")

	if len(failingCIPRs) > 0 {
		fmt.Println("\n🔴 FAILING CI/CD:")
		for _, pr := range failingCIPRs {
			daysStr := fmt.Sprintf("%.1f days ago", now.Sub(pr.UpdatedAt).Hours()/24.0)
			fmt.Printf("  - [%s] %s\n    URL: %s\n    Last Updated: %s (%s)\n", pr.Repository, pr.Title, pr.URL, pr.UpdatedAt.Format("2006-01-02 15:04"), daysStr)
		}
	}

	if len(approvedPRs) > 0 {
		fmt.Println("\n🟢 APPROVED & READY TO MERGE:")
		for _, pr := range approvedPRs {
			daysStr := fmt.Sprintf("%.1f days ago", now.Sub(pr.UpdatedAt).Hours()/24.0)
			fmt.Printf("  - [%s] %s\n    URL: %s\n    Last Updated: %s (%s)\n", pr.Repository, pr.Title, pr.URL, pr.UpdatedAt.Format("2006-01-02 15:04"), daysStr)
		}
	}

	if len(staleOrWaitingPRs) > 0 {
		fmt.Println("\n⏳ STALE OR WAITING ON REVIEWS:")
		for _, pr := range staleOrWaitingPRs {
			daysStr := fmt.Sprintf("%.1f days ago", now.Sub(pr.UpdatedAt).Hours()/24.0)
			reason := fmt.Sprintf("Stale (%d+ days)", staleThresholdDays)
			if pr.ReviewDecision == "REVIEW_REQUIRED" {
				if now.Sub(pr.UpdatedAt) > staleThresholdDays*24*time.Hour {
					reason = fmt.Sprintf("Stale (%d+ days) & Waiting on reviews", staleThresholdDays)
				} else {
					reason = "Waiting on reviews"
				}
			}
			fmt.Printf("  - [%s] %s\n    URL: %s\n    Reason: %s\n    Last Updated: %s (%s)\n", pr.Repository, pr.Title, pr.URL, reason, pr.UpdatedAt.Format("2006-01-02 15:04"), daysStr)
		}
	}

	if len(failingCIPRs) == 0 && len(approvedPRs) == 0 && len(staleOrWaitingPRs) == 0 {
		fmt.Println("\n🎉 All PRs are in a great state! No pull requests currently need attention.")
	}
	fmt.Println("========================================================================")
}

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
	description := fmt.Sprintf("review %s add PR comments on blockers, when it's good to merge approve", fullURL)

	cmd := exec.CommandContext(ctx, "multica", "issue", "create",
		"--assignee", "Claude",
		"--title", title,
		"--description", description,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("multica execution failed: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}
