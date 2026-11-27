package github

import (
	"fmt"
	"slices"
	"sort"
	"strings"

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
	case "merged":
		if r.By != "" {
			return fmt.Sprintf("%s merged by %s, who approves it", r.SliceID, r.By)
		}
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
	return sl.PR != nil && reworking(sl.Status)
}

func reworking(status flow.SliceStatus) bool {
	switch status {
	case flow.SliceQueued, flow.SliceRunning, flow.SliceFlagged, flow.SliceStalled:
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

	known := sl.PR != nil && sl.PR.Repo == snap.Repo && sl.PR.Number == snap.Number
	if snap.State != PROpen && !known {
		note("the only PR on branch %s is %s, which Metronome never recorded for this slice; nothing recorded", sl.Branch, strings.ToLower(string(snap.State)))
		return actions, notes
	}
	if reworking(sl.Status) {
		decision := "nothing recorded, and a person must decide, for example drop the item or reopen the work"
		switch snap.State {
		case PRMerged:
			if snap.MergedBy == "" {
				note("merged while the slice is %s for rework, but GitHub does not say who merged it; %s", sl.Status, decision)
			} else {
				note("merged by %s while the slice is %s for rework; Metronome does not record a merge during rework; %s", snap.MergedBy, sl.Status, decision)
			}
		case PRClosed:
			note("closed without merging while the slice is %s for rework; %s", sl.Status, decision)
		}
		return actions, notes
	}
	switch snap.State {
	case PRClosed:
		note("closed without merging; nothing recorded")
		return actions, notes
	case PRMerged:
		if sl.Status == flow.SliceApproved {
			if sl.PR.HeadSHA == snap.HeadSHA {
				add("merged", "merged", api.GitHubRequest{SliceID: sl.ID})
				return actions, notes
			}
			if snap.MergedBy == "" {
				note("merged at %s, not the approved head %s, and GitHub does not say who merged it; nothing recorded", short(snap.HeadSHA), short(sl.PR.HeadSHA))
				return actions, notes
			}
		}
		switch sl.Status {
		case flow.SliceSubmitted, flow.SlicePROpen, flow.SliceInReview, flow.SliceApproved:
			if snap.MergedBy == "" {
				note("merged before approval, but GitHub does not say who merged it; nothing recorded")
				return actions, notes
			}
			if sl.Status == flow.SliceSubmitted || sl.PR.HeadSHA != snap.HeadSHA {
				add("pr-opened", fmt.Sprintf("opened:%s:v%d", snap.HeadSHA, sl.Version), base)
			}
			add("merged", "merged", api.GitHubRequest{SliceID: sl.ID, By: snap.MergedBy})
		default:
			note("merged while %s in Metronome; nothing recorded", sl.Status)
		}
		return actions, notes
	}

	status := sl.Status
	var recorded flow.PR
	if known {
		recorded = *sl.PR
	}
	if status == flow.SliceApproved && sl.PR.HeadSHA == snap.HeadSHA {
		if !known {
			note("PR #%d on the branch is at the approved head; nothing recorded", snap.Number)
		}
		return actions, notes
	}
	if !known || status == flow.SliceSubmitted || recorded.HeadSHA != snap.HeadSHA {
		add("pr-opened", fmt.Sprintf("opened:%s:v%d", snap.HeadSHA, sl.Version), base)
		if status == flow.SliceApproved {
			note("head moved from %s to %s after approval; recording it withdraws the approval", short(sl.PR.HeadSHA), short(snap.HeadSHA))
		}
		if status == flow.SliceSubmitted || status == flow.SliceApproved {
			status = flow.SlicePROpen
		}
		recorded = flow.PR{Repo: snap.Repo, Number: snap.Number, HeadSHA: snap.HeadSHA}
	}

	switch snap.HeadChecks {
	case ChecksSuccess, ChecksFailure:
		success := snap.HeadChecks == ChecksSuccess
		if !recorded.ChecksRecorded || recorded.ChecksGreen != success {
			req := base
			req.Success = success
			add("checks", fmt.Sprintf("checks:%s:%s:v%d", snap.HeadSHA, snap.HeadChecks, sl.Version), req)
			recorded.ChecksRecorded, recorded.ChecksGreen = true, success
		}
		if !success && (status == flow.SlicePROpen || status == flow.SliceInReview) {
			return actions, notes
		}
	case ChecksNone:
		note("no checks reported on %s; a slice moves into review only on green checks", short(snap.HeadSHA))
	}
	green := recorded.ChecksRecorded && recorded.ChecksGreen

	if status == flow.SlicePROpen && green && !snap.Draft {
		add("ready-for-review", fmt.Sprintf("ready:%s:v%d", snap.HeadSHA, sl.Version), api.GitHubRequest{SliceID: sl.ID})
		status = flow.SliceInReview
	}
	if status != flow.SliceInReview {
		return actions, notes
	}

	latest := latestVerdicts(snap.Reviews, sl.Approvers)
	for _, r := range latest {
		if r.State == ReviewChangesRequested && r.CommitSHA == snap.HeadSHA {
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
		add("approved", fmt.Sprintf("review:%s:v%d", r.ID, sl.Version), req)
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
