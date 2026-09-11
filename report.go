package main

import (
	"fmt"
	"slices"
	"time"
)

const staleThresholdDays = 7

func printAttentionReport(allPRDetails map[string]PullRequestDetail) {
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
		if pr.ReviewDecision == reviewApproved && pr.Mergeable == mergeableMergeable {
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
