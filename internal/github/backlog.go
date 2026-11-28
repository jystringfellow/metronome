package github

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jystringfellow/metronome/internal/flow"
)

var InstanceName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)

const claimPrefix = "metronome:claimed:"

func IssueBacklog(repo, fallbackOwner string, issues []Issue, ranks map[int]float64, instance string) ([]flow.BacklogItem, error) {
	issues = slices.Clone(issues)
	slices.SortStableFunc(issues, func(left, right Issue) int {
		leftRank, leftRanked := ranks[left.Number]
		rightRank, rightRanked := ranks[right.Number]
		if leftRanked != rightRanked {
			if leftRanked {
				return -1
			}
			return 1
		}
		if order := cmp.Compare(leftRank, rightRank); order != 0 {
			return order
		}
		if order := left.CreatedAt.Compare(right.CreatedAt); order != 0 {
			return order
		}
		return cmp.Compare(left.Number, right.Number)
	})
	items := make([]flow.BacklogItem, 0, len(issues))
	seen := map[string]string{}
	var problems []error
	for _, issue := range issues {
		id := fmt.Sprintf("%s#%d", repo, issue.Number)
		ref := bodyRef(issue.Body)
		if ref == "" {
			ref = id
		}
		if previous, exists := seen[ref]; exists {
			problems = append(problems, fmt.Errorf("%s is the ref of %s and %s", ref, previous, id))
		}
		seen[ref] = id
		owner := fallbackOwner
		if len(issue.Assignees) > 0 && issue.Assignees[0] != "" {
			owner = issue.Assignees[0]
		}
		if owner == "" {
			problems = append(problems, fmt.Errorf("%s has no assignee; pass --owner", id))
		}
		if issue.Number <= 0 || strings.TrimSpace(issue.Title) == "" {
			problems = append(problems, fmt.Errorf("%s needs a positive issue number and title", id))
		}
		tier, err := issueTier(issue.Labels)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", id, err))
		}
		var claims []string
		for _, label := range issue.Labels {
			if strings.HasPrefix(strings.ToLower(label), claimPrefix) {
				claimant := label[len(claimPrefix):]
				if claimant != "" && !strings.EqualFold(claimant, instance) {
					claims = append(claims, claimant)
				}
			}
		}
		slices.Sort(claims)
		items = append(items, flow.BacklogItem{Ref: ref, Title: issue.Title, Intent: bodyIntent(issue.Body), Owner: owner, PredictedTier: tier, ClaimedBy: strings.Join(claims, ","), Slices: []flow.SliceSpec{{Title: issue.Title, Repo: repo, Criteria: bodyCriteria(issue.Body), Approvers: []string{}}}})
	}
	if err := errors.Join(problems...); err != nil {
		return nil, err
	}
	return items, nil
}

func issueTier(labels []string) (*int, error) {
	var tier *int
	for _, label := range labels {
		key, value, ok := strings.Cut(label, ":")
		if !ok || !strings.EqualFold(key, "tier") {
			continue
		}
		number, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || number < 0 || number > 3 {
			return nil, fmt.Errorf("invalid tier label %q; want 0 to 3", label)
		}
		if tier != nil && *tier != number {
			return nil, fmt.Errorf("conflicting tier labels")
		}
		tier = &number
	}
	return tier, nil
}
