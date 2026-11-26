package github

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/flow"
)

var t0 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func submitted() flow.Slice {
	return flow.Slice{ID: "I1.1", Repo: "acme/app", Branch: "metronome/I1.1", Status: flow.SliceSubmitted, Submits: 1, Approvers: []string{"rev"}}
}

func inReview(head string) flow.Slice {
	sl := submitted()
	sl.Status = flow.SliceInReview
	sl.PR = &flow.PR{Repo: "acme/app", Number: 7, HeadSHA: head, ChecksGreen: true}
	return sl
}

func open(head string, checks Checks, reviews ...Review) *Snapshot {
	return &Snapshot{Repo: "acme/app", Number: 7, HeadSHA: head, State: PROpen, HeadChecks: checks, Reviews: reviews}
}

func review(id, author string, state ReviewState, commit string, minute int) Review {
	return Review{ID: id, Author: author, State: state, CommitSHA: commit, SubmittedAt: t0.Add(time.Duration(minute) * time.Minute)}
}

func events(actions []Action) []string {
	var out []string
	for _, a := range actions {
		out = append(out, a.Event)
	}
	return out
}

func TestReconcile(t *testing.T) {
	merged := open("aaa", ChecksSuccess)
	merged.State = PRMerged
	closed := open("aaa", ChecksSuccess)
	closed.State = PRClosed
	draft := open("aaa", ChecksSuccess)
	draft.Draft = true
	approvedSlice := inReview("aaa")
	approvedSlice.Status = flow.SliceApproved

	cases := []struct {
		name  string
		slice flow.Slice
		snap  *Snapshot
		want  []string
		note  string
	}{
		{"a submitted slice with a green, ready PR moves into review in one poll", submitted(), open("aaa", ChecksSuccess), []string{"pr-opened", "checks", "ready-for-review"}, ""},
		{"a draft PR stays out of review even when green", submitted(), draft, []string{"pr-opened", "checks"}, ""},
		{"pending checks record only the PR", submitted(), open("aaa", ChecksPending), []string{"pr-opened"}, ""},
		{"failed checks stop the poll at the failure", submitted(), open("aaa", ChecksFailure), []string{"pr-opened", "checks"}, ""},
		{"a PR with no checks never moves into review", submitted(), open("aaa", ChecksNone), []string{"pr-opened"}, "no checks reported"},
		{"no PR for the branch records nothing", submitted(), nil, nil, ""},
		{"an approval on the head from a named approver approves", inReview("aaa"), open("aaa", ChecksSuccess, review("r1", "rev", ReviewApproved, "aaa", 1)), []string{"checks", "approved"}, ""},
		{"an approval from someone not named is ignored", inReview("aaa"), open("aaa", ChecksSuccess, review("r1", "other", ReviewApproved, "aaa", 1)), []string{"checks"}, ""},
		{"a comment is never an approval", inReview("aaa"), open("aaa", ChecksSuccess, review("r1", "rev", ReviewCommented, "aaa", 1)), []string{"checks"}, ""},
		{"an approval of an older commit does not count", inReview("aaa"), open("aaa", ChecksSuccess, review("r1", "rev", ReviewApproved, "old", 1)), []string{"checks"}, "is for old"},
		{"a later change request outweighs an earlier approval", inReview("aaa"), open("aaa", ChecksSuccess, review("r1", "rev", ReviewApproved, "aaa", 1), review("r2", "rev", ReviewChangesRequested, "aaa", 2)), []string{"checks", "changes-requested"}, ""},
		{"a later approval replaces an earlier change request", inReview("aaa"), open("aaa", ChecksSuccess, review("r1", "rev", ReviewChangesRequested, "aaa", 1), review("r2", "rev", ReviewApproved, "aaa", 2)), []string{"checks", "approved"}, ""},
		{"a dismissed review no longer counts", inReview("aaa"), open("aaa", ChecksSuccess, review("r1", "rev", ReviewChangesRequested, "aaa", 1), review("r2", "rev", ReviewDismissed, "aaa", 2)), []string{"checks"}, ""},
		{"a new head in review is recorded before its checks and approval", inReview("aaa"), open("bbb", ChecksSuccess, review("r1", "rev", ReviewApproved, "bbb", 1)), []string{"pr-opened", "checks", "approved"}, ""},
		{"an approval waits for green checks on a new head", inReview("aaa"), open("bbb", ChecksPending, review("r1", "rev", ReviewApproved, "bbb", 1)), []string{"pr-opened"}, "not green"},
		{"a merge after approval records the merge", approvedSlice, merged, []string{"merged"}, ""},
		{"a merge before approval is recorded and reported", inReview("aaa"), merged, []string{"merged"}, "merged while in_review"},
		{"a merge Metronome never saw opened records both", submitted(), merged, []string{"pr-opened", "merged"}, ""},
		{"a PR closed without merging records nothing", inReview("aaa"), closed, nil, "closed without merging"},
		{"a head that moves after approval is reported, not recorded", approvedSlice, open("bbb", ChecksSuccess), nil, "after approval"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			actions, notes := Reconcile(c.slice, c.snap)
			if got := events(actions); !slices.Equal(got, c.want) {
				t.Fatalf("events = %v, want %v (notes %v)", got, c.want, notes)
			}
			if c.note != "" && !strings.Contains(strings.Join(notes, "\n"), c.note) {
				t.Fatalf("notes %v do not mention %q", notes, c.note)
			}
		})
	}
}

func TestAnyReviewerCountsWhenTheSliceNamesNoApprovers(t *testing.T) {
	sl := inReview("aaa")
	sl.Approvers = nil
	actions, _ := Reconcile(sl, open("aaa", ChecksSuccess, review("r1", "anyone", ReviewApproved, "aaa", 1)))
	if got := events(actions); !slices.Equal(got, []string{"checks", "approved"}) {
		t.Fatalf("events = %v", got)
	}
	if actions[1].Request.By != "anyone" {
		t.Fatalf("approved by %q", actions[1].Request.By)
	}
}

func TestRequestIDsChangeWithEachSubmitSoResubmittingTheSameHeadIsRecorded(t *testing.T) {
	first := submitted()
	again := submitted()
	again.Submits = 2
	again.PR = &flow.PR{Repo: "acme/app", Number: 7, HeadSHA: "aaa"}
	a1, _ := Reconcile(first, open("aaa", ChecksSuccess))
	a2, _ := Reconcile(again, open("aaa", ChecksSuccess))
	for i := range a1 {
		if a1[i].RequestID == a2[i].RequestID {
			t.Fatalf("%s request ID %q repeats across submits", a1[i].Event, a1[i].RequestID)
		}
	}
}

func TestOnlyWaitingSlicesWithABranchAreTracked(t *testing.T) {
	for status, want := range map[flow.SliceStatus]bool{
		flow.SliceQueued: false, flow.SliceRunning: false, flow.SliceFlagged: false, flow.SliceStalled: false,
		flow.SliceSubmitted: true, flow.SlicePROpen: true, flow.SliceInReview: true, flow.SliceApproved: true, flow.SliceMerged: false,
	} {
		sl := submitted()
		sl.Status = status
		if Tracked(sl) != want {
			t.Errorf("Tracked(%s) = %v", status, !want)
		}
	}
	sl := submitted()
	sl.Branch = ""
	if Tracked(sl) {
		t.Error("a slice with no branch is tracked")
	}
}
