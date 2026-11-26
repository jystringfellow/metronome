package github

import (
	"fmt"
	"slices"
	"sort"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/flow"
)

type Action struct {
	Event     string
	RequestID string
	Request   api.GitHubRequest
}

func (a Action) String() string {
	r := a.Request
	switch a.Event {
	case "pr-opened":
		return fmt.Sprintf("%s pr-opened %s#%d at %s", r.SliceID, r.Repo, r.Number, short(r.HeadSHA))
	case "checks":
		result := "failed"
		if r.Success {
			result = "green"
		}
		return fmt.Sprintf("%s checks %s at %s", r.SliceID, result, short(r.HeadSHA))
	case "approved":
		return fmt.Sprintf("%s approved by %s at %s", r.SliceID, r.By, short(r.HeadSHA))
	case "changes-requested":
		return fmt.Sprintf("%s changes requested by %s", r.SliceID, r.By)
	}
	return r.SliceID + " " + a.Event
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func Tracked(sl flow.Slice) bool {
	if sl.Branch == "" || sl.Repo == "" {
		return false
	}
	switch sl.Status {
	case flow.SliceSubmitted, flow.SlicePROpen, flow.SliceInReview, flow.SliceApproved:
		return true
	}
	return false
}

func Reconcile(sl flow.Slice, snap *Snapshot) (actions []Action, notes []string) {
	if snap == nil {
		return nil, nil
	}
	pr := snap.Repo + "#" + fmt.Sprint(snap.Number)
	base := api.GitHubRequest{SliceID: sl.ID, Repo: snap.Repo, Number: snap.Number, HeadSHA: snap.HeadSHA}
	id := func(fact string) string {
		return fmt.Sprintf("github:%s:%s:%s", sl.ID, pr, fact)
	}
	add := func(event, fact string, req api.GitHubRequest) {
		actions = append(actions, Action{Event: event, RequestID: id(fact), Request: req})
	}
	note := func(format string, args ...any) {
		notes = append(notes, sl.ID+" "+pr+": "+fmt.Sprintf(format, args...))
	}

	if snap.State == PRClosed {
		note("closed without merging; nothing recorded")
		return actions, notes
	}

	status := sl.Status
	head := ""
	green := false
	if sl.PR != nil {
		head, green = sl.PR.HeadSHA, sl.PR.ChecksGreen
	}

	if status == flow.SliceApproved && head != "" && head != snap.HeadSHA && snap.State == PROpen {
		note("head moved from %s to %s after approval; not recorded", short(head), short(snap.HeadSHA))
		return actions, notes
	}
	if sl.PR == nil || status == flow.SliceSubmitted || (head != snap.HeadSHA && (status == flow.SlicePROpen || status == flow.SliceInReview)) {
		add("pr-opened", fmt.Sprintf("opened:%s:s%d", snap.HeadSHA, sl.Submits), base)
		if status == flow.SliceSubmitted {
			status = flow.SlicePROpen
		}
		head, green = snap.HeadSHA, false
	}

	if snap.State == PRMerged {
		add("merged", "merged", api.GitHubRequest{SliceID: sl.ID})
		if status != flow.SliceApproved {
			note("merged while %s in Metronome", status)
		}
		return actions, notes
	}

	if status == flow.SlicePROpen || status == flow.SliceInReview {
		switch snap.HeadChecks {
		case ChecksSuccess, ChecksFailure:
			success := snap.HeadChecks == ChecksSuccess
			req := base
			req.Success = success
			add("checks", fmt.Sprintf("checks:%s:%s:s%d", snap.HeadSHA, snap.HeadChecks, sl.Submits), req)
			green = success
			if !success && status == flow.SlicePROpen {
				return actions, notes
			}
		case ChecksNone:
			note("no checks reported on %s; a slice moves into review only on green checks", short(snap.HeadSHA))
		}
	}

	if status == flow.SlicePROpen && green {
		if snap.Draft {
			return actions, notes
		}
		add("ready-for-review", fmt.Sprintf("ready:%s:s%d", snap.HeadSHA, sl.Submits), api.GitHubRequest{SliceID: sl.ID})
		status = flow.SliceInReview
	}

	if status != flow.SliceInReview {
		return actions, notes
	}
	latest := latestVerdicts(snap.Reviews, sl.Approvers)
	for _, r := range latest {
		if r.State == ReviewChangesRequested {
			add("changes-requested", "review:"+r.ID, api.GitHubRequest{SliceID: sl.ID, By: r.Author})
			return actions, notes
		}
	}
	for _, r := range latest {
		if r.State != ReviewApproved {
			continue
		}
		if r.CommitSHA != snap.HeadSHA {
			note("approval by %s is for %s, not the head %s", r.Author, short(r.CommitSHA), short(snap.HeadSHA))
			continue
		}
		if !green {
			note("approved by %s but checks on %s are not green", r.Author, short(snap.HeadSHA))
			return actions, notes
		}
		req := base
		req.By = r.Author
		add("approved", "review:"+r.ID, req)
		return actions, notes
	}
	return actions, notes
}

func latestVerdicts(reviews []Review, approvers []string) []Review {
	sorted := slices.Clone(reviews)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].SubmittedAt.Before(sorted[j].SubmittedAt) })
	byAuthor := map[string]Review{}
	for _, r := range sorted {
		if len(approvers) > 0 && !slices.Contains(approvers, r.Author) {
			continue
		}
		switch r.State {
		case ReviewApproved, ReviewChangesRequested, ReviewDismissed:
			byAuthor[r.Author] = r
		}
	}
	var out []Review
	for _, r := range byAuthor {
		if r.State != ReviewDismissed {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Author < out[j].Author })
	return out
}
