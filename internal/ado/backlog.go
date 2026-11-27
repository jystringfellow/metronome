package ado

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/github"
)

type Result struct {
	Items        []flow.BacklogItem
	Notes        []string
	Predecessors map[string][]string
}

const DefaultReadyStates = "New,Approved,To Do,Proposed"

var repoName = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9_.-]+$`)

func finished(state string) bool {
	return slices.ContainsFunc([]string{"Done", "Closed", "Removed"}, func(value string) bool { return strings.EqualFold(value, state) })
}

func ref(id int) string { return fmt.Sprintf("AB#%d", id) }

func repoBlocks(blocks []block, repo *string) ([]block, error) {
	var result []block
	for _, entry := range blocks {
		key, value, ok := strings.Cut(entry.text, ":")
		if !ok || !strings.EqualFold(key, "Repo") {
			result = append(result, entry)
			continue
		}
		value = strings.TrimSpace(value)
		if !repoName.MatchString(value) || strings.HasSuffix(value, "/.") || strings.HasSuffix(value, "/..") {
			return nil, fmt.Errorf("invalid Repo: %q; want OWNER/NAME", value)
		}
		if *repo != "" && *repo != value {
			return nil, fmt.Errorf("conflicting Repo: lines")
		}
		*repo = value
	}
	return result, nil
}

func Backlog(items []WorkItem, owner string, ready []string) (Result, error) {
	items = slices.Clone(items)
	slices.SortFunc(items, func(left, right WorkItem) int {
		if left.Rank != nil && right.Rank == nil {
			return -1
		}
		if left.Rank == nil && right.Rank != nil {
			return 1
		}
		if left.Rank != nil && right.Rank != nil {
			if order := cmp.Compare(*left.Rank, *right.Rank); order != 0 {
				return order
			}
		}
		return cmp.Compare(left.ID, right.ID)
	})
	result := Result{Items: make([]flow.BacklogItem, 0, len(items)), Predecessors: map[string][]string{}}
	states := map[int]string{}
	for _, work := range items {
		states[work.ID] = work.State
	}
	var problems []error
	for _, work := range items {
		id := ref(work.ID)
		if finished(work.State) {
			result.Notes = append(result.Notes, fmt.Sprintf("%s is %s; finished, not offered", id, work.State))
			continue
		}
		backlog := flow.BacklogItem{Ref: id, Title: work.Title, Owner: work.AssignedTo, ClaimedBy: work.AssignedTo}
		if backlog.Owner == "" {
			backlog.Owner = owner
		}
		if backlog.Owner == "" {
			problems = append(problems, fmt.Errorf("%s has no Assigned To; pass --owner", id))
		}
		if strings.TrimSpace(work.Title) == "" {
			problems = append(problems, fmt.Errorf("%s needs a title", id))
		}
		if backlog.ClaimedBy == "" && !slices.ContainsFunc(ready, func(state string) bool { return strings.EqualFold(state, work.State) }) {
			backlog.ClaimedBy = "state " + work.State
		}
		tier, err := github.TierLabel(work.Tags)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", id, err))
		}
		backlog.PredictedTier = tier
		for _, tag := range work.Tags {
			key, _, ok := strings.Cut(tag, ":")
			if ok && strings.EqualFold(key, "tier") {
				continue
			}
			if !slices.Contains(backlog.Labels, tag) {
				backlog.Labels = append(backlog.Labels, tag)
			}
		}
		var repo string
		description, err := repoBlocks(htmlBlocks(work.Description), &repo)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", id, err))
		}
		criteria, err := repoBlocks(htmlBlocks(work.AcceptanceCriteria), &repo)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", id, err))
		}
		for _, entry := range description {
			if entry.kind == paragraph {
				backlog.Intent = github.CapIntent(entry.text)
				break
			}
		}
		if repo != "" {
			checks := []string{}
			kind := paragraph
			if slices.ContainsFunc(criteria, func(entry block) bool { return entry.kind == item }) {
				kind = item
			}
			for _, entry := range criteria {
				if entry.kind == kind {
					checks = append(checks, entry.text)
				}
			}
			backlog.Slices = []flow.SliceSpec{{Title: work.Title, Repo: repo, Criteria: checks, Approvers: []string{}}}
		}
		for _, predecessor := range work.Predecessors {
			pred := ref(predecessor)
			result.Predecessors[id] = append(result.Predecessors[id], pred)
			if state, ok := states[predecessor]; !ok {
				result.Notes = append(result.Notes, fmt.Sprintf("%s waits on %s, which is outside the query result", id, pred))
			} else if !finished(state) {
				result.Notes = append(result.Notes, fmt.Sprintf("%s waits on %s (in this result); items cannot depend on items yet, so the link is not carried", id, pred))
			}
		}
		result.Items = append(result.Items, backlog)
	}
	if err := errors.Join(problems...); err != nil {
		return Result{}, err
	}
	return result, nil
}
