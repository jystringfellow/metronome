package cli

import (
	"context"
	"flag"
	"regexp"
	"strings"

	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/github"
)

var issueRepo = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9_.-]+$`)

func (a *app) githubIssues(ctx context.Context, args []string) error {
	fs := newFlags("github issues")
	repo := fs.String("repo", "", "")
	label := fs.String("label", "metronome", "")
	project := fs.String("project", "", "")
	field := fs.String("rank-field", "Rank", "")
	owner := fs.String("owner", "", "")
	instance := fs.String("instance", "", "")
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	if !issueRepo.MatchString(*repo) || strings.HasSuffix(*repo, "/.") || strings.HasSuffix(*repo, "/..") {
		return usagef("--repo must be OWNER/NAME")
	}
	if strings.TrimSpace(*label) == "" || strings.Contains(*label, ",") {
		return usagef("--label must be one nonempty label")
	}
	if *instance != "" && !github.InstanceName.MatchString(*instance) {
		return usagef("invalid --instance")
	}
	var projectSet, fieldSet bool
	fs.Visit(func(f *flag.Flag) {
		projectSet = projectSet || f.Name == "project"
		fieldSet = fieldSet || f.Name == "rank-field"
	})
	var selected github.Project
	if projectSet {
		var err error
		selected, err = github.ParseProject(*project)
		if err != nil {
			return usagef("%v", err)
		}
		if strings.TrimSpace(*field) == "" {
			return usagef("--rank-field cannot be empty")
		}
	} else if fieldSet {
		return usagef("--rank-field requires --project")
	}
	issues, err := a.issues.OpenIssues(ctx, *repo, *label)
	if err != nil {
		return err
	}
	linked, err := a.issues.LinkedPRs(ctx, *repo, *label)
	if err != nil {
		return err
	}
	var ranks map[int]float64
	if projectSet {
		ranks, err = a.issues.ProjectRanks(ctx, selected, *field, *repo)
		if err != nil {
			return err
		}
	}
	items, err := github.IssueBacklog(*repo, strings.TrimSpace(*owner), issues, ranks, *instance, linked)
	if err != nil {
		return err
	}
	return writeJSON(a.out, struct {
		Items []flow.BacklogItem `json:"items"`
	}{items})
}
