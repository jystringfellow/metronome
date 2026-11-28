package cli

import (
	"context"
	"fmt"

	"github.com/jystringfellow/metronome/internal/github"
)

func (a *app) githubAdopt(ctx context.Context, args []string) error {
	fs := newFlags("github adopt")
	ws := fs.String("workstream", "", "")
	if err := parse(fs, args, 1); err != nil {
		return err
	}
	repo, number, err := github.ParsePRRef(fs.Arg(0))
	if err != nil {
		return usagef("%v", err)
	}
	ref := github.FormatPRRef(repo, number)
	if a.pullRequests == nil {
		return fmt.Errorf("GitHub PR source is unavailable")
	}
	pr, err := a.pullRequests.PR(ctx, repo, number)
	if err != nil {
		return err
	}
	if pr == nil {
		return fmt.Errorf("PR %s not found", ref)
	}
	req, err := github.AdoptionFor(pr)
	if err != nil {
		return err
	}
	req.Workstream = *ws
	id := a.requestID
	if id == "" {
		id = "github:adopt:" + ref
	}
	poller := github.Poller{Client: a.client, Source: a.prs, Adopt: a.pullRequests}
	action, notes, err := poller.AdoptOne(ctx, req, id)
	if err != nil {
		return err
	}
	for _, note := range notes {
		fmt.Fprintln(a.out, "note "+note)
	}
	if action == nil {
		return nil
	}
	fmt.Fprintln(a.out, "recorded "+action.String())
	result, err := poller.Follow(ctx, action.Request.SliceID)
	if err != nil {
		return err
	}
	return a.printPoll(result)
}
