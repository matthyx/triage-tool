package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"slices"
	"sync"
	"time"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/gammazero/workerpool"
)

const (
	bugTrackingBoard = "4"
	prTrackingBoard  = "5"
)

var myTeam = []string{"matthyx", "entlein"}

func Run(ctx context.Context, client GHClient) {
	start := time.Now()
	defer func() {
		fmt.Printf("Total time: %v\n", time.Since(start))
	}()

	allIssues, allPulls, allPRDetails, bugItems, prItems := fetchTriageData(ctx, client, 100)
	issueURLs, prURLs := findUntrackedItems(allIssues, allPulls, bugItems, prItems)
	bugProjectID, prProjectID := getProjectIDs(ctx, client)
	newPRItems := addUntrackedItems(ctx, client, bugProjectID, prProjectID, issueURLs, prURLs, allPRDetails)
	prItems = append(prItems, newPRItems...)
	archiveClosedItems(ctx, client, bugProjectID, prProjectID, bugItems, prItems)
	routeOpenPRs(ctx, client, prProjectID, prItems, allPRDetails)
	printAttentionReport(allPRDetails)
}

func fetchTriageData(ctx context.Context, client GHClient, limit int) (allIssues, allPulls mapset.Set[string], allPRDetails map[string]PullRequestDetail, bugItems, prItems []ProjectItem) {
	var errI, errP error

	var initWG sync.WaitGroup

	initWG.Go(func() {
		var boardWG sync.WaitGroup
		boardWG.Go(func() {
			bugItems, errI = client.GetProjectItemsWithState(ctx, "kubescape", bugTrackingBoard, limit)
		})
		boardWG.Go(func() {
			prItems, errP = client.GetProjectItemsWithState(ctx, "kubescape", prTrackingBoard, limit)
		})
		boardWG.Wait()
	})

	initWG.Go(func() {
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

		allIssues = mapset.NewSet[string]()
		allPulls = mapset.NewSet[string]()
		allPRDetails = make(map[string]PullRequestDetail)

		for issues := range issueChan {
			allIssues.Append(issues...)
		}
		for pulls := range pullChan {
			for _, pr := range pulls {
				allPulls.Add(pr.URL)
				allPRDetails[pr.URL] = pr
			}
		}
	})

	initWG.Wait()

	if errI != nil {
		panic(errI)
	}
	if errP != nil {
		panic(errP)
	}

	return
}

func findUntrackedItems(allIssues, allPulls mapset.Set[string], bugItems, prItems []ProjectItem) (issueURLs, prURLs []string) {
	fmt.Println("issues", allIssues.Cardinality())
	fmt.Println("pulls", allPulls.Cardinality())

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

	issueURLs = make([]string, 0, untrackedIssues.Cardinality())
	untrackedIssues.Each(func(url string) bool {
		issueURLs = append(issueURLs, url)
		return false
	})

	prURLs = make([]string, 0, untrackedPulls.Cardinality())
	untrackedPulls.Each(func(url string) bool {
		prURLs = append(prURLs, url)
		return false
	})

	return
}

func getProjectIDs(ctx context.Context, client GHClient) (bugProjectID, prProjectID string) {
	var errBug, errPR error
	var wg sync.WaitGroup
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

	return
}

func addUntrackedItems(ctx context.Context, client GHClient, bugProjectID, prProjectID string, issueURLs, prURLs []string, allPRDetails map[string]PullRequestDetail) []ProjectItem {
	var wg sync.WaitGroup
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

	return newPRItems

}

func archiveClosedItems(ctx context.Context, client GHClient, bugProjectID, prProjectID string, bugItems, prItems []ProjectItem) {
	var wg sync.WaitGroup
	var errBug, errPR error
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

}

func routeOpenPRs(ctx context.Context, client GHClient, prProjectID string, prItems []ProjectItem, allPRDetails map[string]PullRequestDetail) {
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

}
