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
	return flow.Slice{ID: "I1.1", Repo: "acme/app", Branch: "metronome/I1.1", Status: flow.SliceSubmitted, Version: 3, Approvers: []string{"rev"}}
}

func inReview(head string) flow.Slice {
	sl := submitted()
	sl.Status = flow.SliceInReview
	sl.PR = &flow.PR{Repo: "acme/app", Number: 7, HeadSHA: head, ChecksGreen: true, ChecksRecorded: true}
	return sl
}

func stalled() flow.Slice {
	return reworked(flow.SliceStalled)
}

func reworked(status flow.SliceStatus) flow.Slice {
	sl := inReview("aaa")
	sl.Status, sl.Rework, sl.PR.ChecksGreen = status, true, false
	return sl
}

func reviewers(sl flow.Slice, names ...string) flow.Slice {
	sl.Approvers = names
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
	merged.State, merged.MergedBy = PRMerged, "pat"
	mergedNewHead := open("bbb", ChecksSuccess)
	mergedNewHead.State, mergedNewHead.MergedBy = PRMerged, "pat"
	mergedByNobody := open("aaa", ChecksSuccess)
	mergedByNobody.State = PRMerged
	prOpen := inReview("aaa")
	prOpen.Status = flow.SlicePROpen
	closed := open("aaa", ChecksSuccess)
	closed.State = PRClosed
	draft := open("aaa", ChecksSuccess)
	draft.Draft = true
	approvedSlice := inReview("aaa")
	approvedSlice.Status = flow.SliceApproved
	approvedSlice.PR.ApprovedSHA = "aaa"
	movedDraft := open("bbb", ChecksSuccess)
	movedDraft.Draft = true
	movedMergeByNobody := open("bbb", ChecksSuccess)
	movedMergeByNobody.State = PRMerged

	type reconcileCase struct {
		name  string
		slice flow.Slice
		snap  *Snapshot
		want  []string
		note  string
	}
	cases := []reconcileCase{
		{"a submitted slice with a green, ready PR moves into review in one poll", submitted(), open("aaa", ChecksSuccess), []string{"pr-opened", "checks", "ready-for-review"}, ""},
		{"a draft PR stays out of review even when green", submitted(), draft, []string{"pr-opened", "checks"}, ""},
		{"pending checks record only the PR", submitted(), open("aaa", ChecksPending), []string{"pr-opened"}, ""},
		{"failed checks stop the poll at the failure", submitted(), open("aaa", ChecksFailure), []string{"pr-opened", "checks"}, ""},
		{"a PR with no checks never moves into review", submitted(), open("aaa", ChecksNone), []string{"pr-opened"}, "no checks reported"},
		{"no PR for the branch records nothing", submitted(), nil, nil, ""},
		{"checks already recorded for the head are not recorded again", inReview("aaa"), open("aaa", ChecksSuccess), nil, ""},
		{"a check result that changes on the same head is recorded", inReview("aaa"), open("aaa", ChecksFailure), []string{"checks"}, ""},
		{"red checks on the head of a slice in review stop the poll at the failure", inReview("aaa"), open("aaa", ChecksFailure, review("r1", "rev", ReviewChangesRequested, "aaa", 1)), []string{"checks"}, ""},
		{"a new red head in review is recorded with its failure", inReview("aaa"), open("bbb", ChecksFailure), []string{"pr-opened", "checks"}, ""},
		{"an approval on the head from a named approver approves", inReview("aaa"), open("aaa", ChecksSuccess, review("r1", "rev", ReviewApproved, "aaa", 1)), []string{"approved"}, ""},
		{"an approval from someone not named is ignored", inReview("aaa"), open("aaa", ChecksSuccess, review("r1", "other", ReviewApproved, "aaa", 1)), nil, ""},
		{"a comment is never an approval", inReview("aaa"), open("aaa", ChecksSuccess, review("r1", "rev", ReviewCommented, "aaa", 1)), nil, ""},
		{"an approval of an older commit does not count", inReview("aaa"), open("aaa", ChecksSuccess, review("r1", "rev", ReviewApproved, "old", 1)), nil, "is for old"},
		{"a later change request outweighs an earlier approval", inReview("aaa"), open("aaa", ChecksSuccess, review("r1", "rev", ReviewApproved, "aaa", 1), review("r2", "rev", ReviewChangesRequested, "aaa", 2)), []string{"changes-requested"}, ""},
		{"a later approval replaces an earlier change request", inReview("aaa"), open("aaa", ChecksSuccess, review("r1", "rev", ReviewChangesRequested, "aaa", 1), review("r2", "rev", ReviewApproved, "aaa", 2)), []string{"approved"}, ""},
		{"a dismissed review no longer counts", inReview("aaa"), open("aaa", ChecksSuccess, review("r1", "rev", ReviewChangesRequested, "aaa", 1), review("r2", "rev", ReviewDismissed, "aaa", 2)), nil, ""},
		{"a change request on an older head does not hold the slice", reviewers(inReview("bbb"), "rev", "sam"), open("bbb", ChecksSuccess, review("r1", "rev", ReviewChangesRequested, "aaa", 1), review("r2", "sam", ReviewApproved, "bbb", 2)), []string{"approved"}, ""},
		{"a new head in review is recorded before its checks and approval", inReview("aaa"), open("bbb", ChecksSuccess, review("r1", "rev", ReviewApproved, "bbb", 1)), []string{"pr-opened", "checks", "approved"}, ""},
		{"a head pushed back to an earlier commit is recorded again", inReview("bbb"), open("aaa", ChecksSuccess, review("r1", "rev", ReviewApproved, "aaa", 1)), []string{"pr-opened", "checks", "approved"}, ""},
		{"an approval waits for green checks on a new head", inReview("aaa"), open("bbb", ChecksPending, review("r1", "rev", ReviewApproved, "bbb", 1)), []string{"pr-opened"}, "not green"},
		{"a replacement PR on the same head is recorded", inReview("aaa"), &Snapshot{Repo: "acme/app", Number: 9, HeadSHA: "aaa", State: PROpen, HeadChecks: ChecksSuccess}, []string{"pr-opened", "checks"}, ""},
		{"a merge after approval records the merge", approvedSlice, merged, []string{"merged"}, ""},
		{"a merge in review is recorded and approves the slice", inReview("aaa"), merged, []string{"merged"}, ""},
		{"a merge before review is recorded and approves the slice", prOpen, merged, []string{"merged"}, ""},
		{"a merge of a head Metronome has not seen records the head first", inReview("aaa"), mergedNewHead, []string{"pr-opened", "merged"}, ""},
		{"a merge with no merger is reported, not recorded", inReview("aaa"), mergedByNobody, nil, "who merged"},
		{"a merge while the slice is reworked is reported, not recorded", reworked(flow.SliceQueued), merged, nil, "merged by pat while the slice is queued for rework"},
		{"a stalled slice with no PR snapshot records nothing", stalled(), nil, nil, ""},
		{"a stalled slice's open PR records nothing, even with failed checks on its head", stalled(), open("aaa", ChecksFailure), nil, ""},
		{"a stalled slice fixed outside a run returns to review and can be approved on the new head", stalled(), open("bbb", ChecksSuccess, review("r1", "rev", ReviewApproved, "bbb", 1)), []string{"rework-fixed", "approved"}, "who pushed"},
		{"a stalled slice's unchecked head is ignored", stalled(), open("bbb", ChecksNone), nil, "no checks reported"},
		{"a replacement open PR on a stalled slice's branch is ignored", stalled(), &Snapshot{Repo: "acme/app", Number: 9, HeadSHA: "bbb", State: PROpen, HeadChecks: ChecksSuccess}, nil, ""},
		{"a merge of a stalled slice's PR is reported for a person to decide", stalled(), merged, nil, "a person must decide"},
		{"a merge of a stalled slice's PR with no merger is reported, not recorded", stalled(), mergedByNobody, nil, "who merged"},
		{"a close of a stalled slice's PR is reported for a person to decide", stalled(), closed, nil, "a person must decide"},
		{"an old merged PR on a stalled slice's branch is ignored", stalled(), &Snapshot{Repo: "acme/app", Number: 9, State: PRMerged, MergedBy: "pat"}, nil, "never recorded"},
		{"an old closed PR on a stalled slice's branch is ignored", stalled(), &Snapshot{Repo: "acme/app", Number: 9, State: PRClosed}, nil, "never recorded"},
		{"an old merged PR on a reused branch is ignored", submitted(), merged, nil, "never recorded"},
		{"a PR closed without merging records nothing", inReview("aaa"), closed, nil, "closed without merging"},
		{"a green head that moves after approval withdraws it", approvedSlice, open("bbb", ChecksSuccess), []string{"pr-opened", "checks", "ready-for-review"}, "withdraws the approval"},
		{"a pending head withdraws approval", approvedSlice, open("bbb", ChecksPending), []string{"pr-opened"}, ""},
		{"a failed head withdraws approval", approvedSlice, open("bbb", ChecksFailure), []string{"pr-opened", "checks"}, ""},
		{"a moved head can be freshly approved", approvedSlice, open("bbb", ChecksSuccess, review("r2", "rev", ReviewApproved, "bbb", 2)), []string{"pr-opened", "checks", "ready-for-review", "approved"}, ""},
		{"the old approval cannot approve a moved head", approvedSlice, open("bbb", ChecksSuccess, review("r1", "rev", ReviewApproved, "aaa", 1)), []string{"pr-opened", "checks", "ready-for-review"}, "is for aaa"},
		{"a moved draft head stays out of review", approvedSlice, movedDraft, []string{"pr-opened", "checks"}, ""},
		{"the same approved head records nothing", approvedSlice, open("aaa", ChecksSuccess), nil, ""},
		{"another PR at the approved head records nothing", approvedSlice, &Snapshot{Repo: "acme/app", Number: 9, HeadSHA: "aaa", State: PROpen, HeadChecks: ChecksSuccess}, nil, "approved head"},
		{"another PR at a moved head withdraws approval", approvedSlice, &Snapshot{Repo: "acme/app", Number: 9, HeadSHA: "bbb", State: PROpen, HeadChecks: ChecksSuccess}, []string{"pr-opened", "checks", "ready-for-review"}, "withdraws the approval"},
		{"a merge at a moved head records the head then the merge", approvedSlice, mergedNewHead, []string{"pr-opened", "merged"}, ""},
		{"a merge at a moved head with no merger records nothing", approvedSlice, movedMergeByNobody, nil, "who merged"},
	}
	for _, status := range []flow.SliceStatus{flow.SliceQueued, flow.SliceStalled, flow.SliceRunning, flow.SliceFlagged} {
		prefix := string(status) + ": "
		sl := reworked(status)
		var fixed, approved []string
		var fixedNote, uncheckedNote string
		mergeNote := "a person must decide"
		var fixedMerge []string
		if status == flow.SliceQueued || status == flow.SliceStalled {
			fixed, approved = []string{"rework-fixed"}, []string{"rework-fixed", "approved"}
			fixedNote, uncheckedNote = "who pushed", "no checks reported"
			fixedMerge, mergeNote = []string{"rework-fixed", "merged"}, "who pushed"
			cases = append(cases,
				reconcileCase{prefix + "new change request", sl, open("bbb", ChecksSuccess, review("r2", "rev", ReviewChangesRequested, "bbb", 2)), []string{"rework-fixed", "changes-requested"}, fixedNote},
				reconcileCase{prefix + "old change request", sl, open("bbb", ChecksSuccess, review("r1", "rev", ReviewChangesRequested, "aaa", 1)), fixed, fixedNote},
			)
		}
		redMerge := open("bbb", ChecksFailure)
		redMerge.State, redMerge.MergedBy = PRMerged, "pat"
		nonRework := sl
		nonRework.Rework = false
		cases = append(cases,
			reconcileCase{prefix + "no PR snapshot", sl, nil, nil, ""},
			reconcileCase{prefix + "failed checks", sl, open("aaa", ChecksFailure), nil, ""},
			reconcileCase{prefix + "new green, approved head", sl, open("bbb", ChecksSuccess, review("r1", "rev", ReviewApproved, "bbb", 1)), approved, fixedNote},
			reconcileCase{prefix + "new green head", sl, open("bbb", ChecksSuccess), fixed, fixedNote},
			reconcileCase{prefix + "same green head", sl, open("aaa", ChecksSuccess), nil, ""},
			reconcileCase{prefix + "new pending head", sl, open("bbb", ChecksPending), nil, ""},
			reconcileCase{prefix + "new red head", sl, open("bbb", ChecksFailure), nil, ""},
			reconcileCase{prefix + "unchecked head", sl, open("bbb", ChecksNone), nil, uncheckedNote},
			reconcileCase{prefix + "new green merged head", sl, mergedNewHead, fixedMerge, mergeNote},
			reconcileCase{prefix + "new merged head without merger", sl, movedMergeByNobody, nil, "who merged"},
			reconcileCase{prefix + "new red merged head", sl, redMerge, nil, "a person must decide"},
			reconcileCase{prefix + "not rework", nonRework, open("bbb", ChecksSuccess), nil, ""},
			reconcileCase{prefix + "new green draft head", sl, movedDraft, nil, ""},
			reconcileCase{prefix + "replacement open PR", sl, &Snapshot{Repo: "acme/app", Number: 9, HeadSHA: "bbb", State: PROpen, HeadChecks: ChecksSuccess}, nil, ""},
			reconcileCase{prefix + "merge", sl, merged, nil, "a person must decide"},
			reconcileCase{prefix + "merge with no merger", sl, mergedByNobody, nil, "who merged"},
			reconcileCase{prefix + "close", sl, closed, nil, "a person must decide"},
			reconcileCase{prefix + "old merged PR", sl, &Snapshot{Repo: "acme/app", Number: 9, State: PRMerged, MergedBy: "pat"}, nil, "never recorded"},
			reconcileCase{prefix + "old closed PR", sl, &Snapshot{Repo: "acme/app", Number: 9, State: PRClosed}, nil, "never recorded"},
		)
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
			if (reworking(c.slice.Status) || c.name == "the same approved head records nothing") && c.note == "" && len(notes) != 0 {
				t.Fatalf("notes = %v, want none", notes)
			}
		})
	}
}

func TestReworkFixedAction(t *testing.T) {
	snap := open("cccccccc", ChecksSuccess)
	snap.HeadAuthor = "sam"
	actions, notes := Reconcile(stalled(), snap)
	if len(actions) != 1 || len(notes) != 0 || actions[0].Request.By != "sam" || actions[0].RequestID != "github:I1.1:acme/app#7:fixed:cccccccc:v3" || actions[0].String() != "I1.1 back in review at ccccccc, fixed outside a run by sam" {
		t.Fatalf("actions = %+v, notes = %v", actions, notes)
	}
	snap.HeadAuthor = ""
	actions, notes = Reconcile(stalled(), snap)
	if len(actions) != 1 || actions[0].String() != "I1.1 back in review at ccccccc, fixed outside a run" || len(notes) != 1 || !strings.Contains(notes[0], "who pushed") {
		t.Fatalf("actions = %+v, notes = %v", actions, notes)
	}
}

func TestAnyReviewerCountsWhenTheSliceNamesNoApprovers(t *testing.T) {
	sl := inReview("aaa")
	sl.Approvers = nil
	actions, _ := Reconcile(sl, open("aaa", ChecksSuccess, review("r1", "anyone", ReviewApproved, "aaa", 1)))
	if got := events(actions); !slices.Equal(got, []string{"approved"}) {
		t.Fatalf("events = %v", got)
	}
	if actions[0].Request.By != "anyone" {
		t.Fatalf("approved by %q", actions[0].Request.By)
	}
}

func TestAMergeBeforeApprovalNamesTheMerger(t *testing.T) {
	snap := open("aaa", ChecksSuccess)
	snap.State, snap.MergedBy = PRMerged, "pat"
	actions, _ := Reconcile(inReview("aaa"), snap)
	if len(actions) != 1 || actions[0].Request.By != "pat" {
		t.Fatalf("actions = %+v, want one merge by pat", actions)
	}
}

func TestAMergeAtAMovedHeadNamesTheMerger(t *testing.T) {
	sl := inReview("aaa")
	sl.Status, sl.PR.ApprovedSHA = flow.SliceApproved, "aaa"
	snap := open("bbb", ChecksSuccess)
	snap.State, snap.MergedBy = PRMerged, "pat"
	actions, _ := Reconcile(sl, snap)
	if !slices.Equal(events(actions), []string{"pr-opened", "merged"}) || actions[0].Request.HeadSHA != "bbb" || actions[1].Request.By != "pat" {
		t.Fatalf("actions = %+v, want new head followed by merge by pat", actions)
	}
	snap.HeadSHA = "aaa"
	actions, _ = Reconcile(sl, snap)
	if !slices.Equal(events(actions), []string{"merged"}) || actions[0].Request.By != "" {
		t.Fatalf("actions = %+v, want only the merge without a new approval", actions)
	}
}

func TestAMergeOrCloseDuringReworkNamesTheSliceThePRWhatHappenedAndTheMerger(t *testing.T) {
	for _, status := range []flow.SliceStatus{flow.SliceQueued, flow.SliceRunning, flow.SliceFlagged, flow.SliceStalled} {
		for _, scenario := range []struct {
			state    PRState
			merger   string
			expected string
		}{
			{PRMerged, "pat", "merged by pat"},
			{PRMerged, "", "who merged"},
			{PRClosed, "", "closed without merging"},
		} {
			t.Run(string(status)+"/"+scenario.expected, func(t *testing.T) {
				snap := open("aaa", ChecksFailure)
				snap.State, snap.MergedBy = scenario.state, scenario.merger
				actions, notes := Reconcile(reworked(status), snap)
				if len(actions) != 0 || len(notes) != 1 {
					t.Fatalf("actions = %v, notes = %v, want one note only", actions, notes)
				}
				for _, want := range []string{"I1.1", "acme/app#7", scenario.expected, string(status) + " for rework", "a person must decide"} {
					if !strings.Contains(notes[0], want) {
						t.Errorf("note %q does not mention %q", notes[0], want)
					}
				}
			})
		}
	}
}

func TestRequestIDsChangeWithTheSliceVersionSoTheSameFactCanBeRecordedAgain(t *testing.T) {
	earlier := submitted()
	later := submitted()
	later.Version = earlier.Version + 4
	a1, _ := Reconcile(earlier, open("aaa", ChecksSuccess))
	a2, _ := Reconcile(later, open("aaa", ChecksSuccess))
	for i := range a1 {
		if a1[i].RequestID == a2[i].RequestID {
			t.Fatalf("%s request ID %q repeats across slice versions", a1[i].Event, a1[i].RequestID)
		}
	}
}

func TestOnlyWaitingSlicesAndReworkedSlicesWithAPRAreTracked(t *testing.T) {
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
	for _, status := range []flow.SliceStatus{flow.SliceQueued, flow.SliceRunning, flow.SliceFlagged, flow.SliceStalled} {
		sl := reworked(status)
		if !Tracked(sl) {
			t.Errorf("a %s slice with a PR is not tracked", status)
		}
		sl.Branch = ""
		if Tracked(sl) {
			t.Errorf("a %s slice with a PR but no branch is tracked", status)
		}
		sl = reworked(status)
		sl.Repo = ""
		if Tracked(sl) {
			t.Errorf("a %s slice with a PR but no repo is tracked", status)
		}
	}
	if Tracked(reworked(flow.SliceMerged)) {
		t.Error("a merged slice with a PR is tracked")
	}
}
