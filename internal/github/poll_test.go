package github_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/github"
	"github.com/jystringfellow/metronome/internal/store"
)

type handlerTransport struct{ h http.Handler }

func (ht handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	ht.h.ServeHTTP(rec, r)
	return rec.Result(), nil
}

type countingTransport struct {
	handlerTransport
	posts int
}

func (ct *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPost {
		ct.posts++
	}
	return ct.handlerTransport.RoundTrip(r)
}

type fakeGitHub map[string]*github.Snapshot

func (f fakeGitHub) PRForBranch(_ context.Context, repo, branch string) (*github.Snapshot, error) {
	if branch == "metronome/broken" {
		return nil, errors.New("gh is not signed in")
	}
	return f[repo+" "+branch], nil
}

func must(t *testing.T, _ flow.Result, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func submittedSlice(t *testing.T, eng *flow.Engine) {
	ctx := context.Background()
	r, err := eng.SyncBacklog(ctx, "pat", "", "", []flow.BacklogItem{{Ref: "ACME-1", Title: "Add export", Owner: "pat", PredictedTier: 2}})
	must(t, r, err)
	r, err = eng.StartPlan(ctx, "agent", "", "", "")
	must(t, r, err)
	r, err = eng.ProposePlan(ctx, "agent", "", "I1", []flow.SliceSpec{{Title: "API endpoint", Repo: "acme/app", Approvers: []string{"rev"}}}, "")
	must(t, r, err)
	r, err = eng.ApprovePlan(ctx, "pat", "", "I1", nil)
	must(t, r, err)
	r, err = eng.Claim(ctx, "laptop", "", "laptop", "", "")
	must(t, r, err)
	r, err = eng.Submit(ctx, "laptop", "", "R1", "metronome/I1.1")
	must(t, r, err)
}

func slice(t *testing.T, eng *flow.Engine) flow.Slice {
	var sl flow.Slice
	eng.View(func(s *flow.State) { sl = *s.Slices["I1.1"] })
	return sl
}

func TestAClosedDependencyPRIsReportedForTheSlicesThatWait(t *testing.T) {
	ctx := context.Background()
	eng, err := flow.NewEngine(ctx, flow.DefaultConfig(), store.NewMemory(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := eng.SyncBacklog(ctx, "pat", "", "", []flow.BacklogItem{{Ref: "ACME-1", Title: "Migration", Owner: "pat"}})
	must(t, result, err)
	result, err = eng.StartPlan(ctx, "agent", "", "", "I1")
	must(t, result, err)
	result, err = eng.ProposePlan(ctx, "agent", "", "I1", []flow.SliceSpec{{Title: "Schema", Repo: "acme/app"}, {Title: "Backfill", Repo: "acme/app", After: []int{1}}}, "")
	must(t, result, err)
	result, err = eng.ApprovePlan(ctx, "pat", "", "I1", nil)
	must(t, result, err)
	result, err = eng.Claim(ctx, "laptop", "", "laptop", "", "")
	must(t, result, err)
	result, err = eng.Submit(ctx, "laptop", "", "R1", "metronome/I1.1")
	must(t, result, err)
	result, err = eng.PROpened(ctx, "github", "", "I1.1", "acme/app", 7, "aaa")
	must(t, result, err)
	snap := &github.Snapshot{Repo: "acme/app", Number: 7, HeadSHA: "aaa", State: github.PRClosed}
	p := github.Poller{
		Client: &api.Client{BaseURL: "http://metronome.test", Actor: "github", HTTP: &http.Client{Transport: handlerTransport{api.Handler(eng)}}},
		Source: fakeGitHub{"acme/app metronome/I1.1": snap},
	}
	checkWaiting := func(want []string) {
		t.Helper()
		eng.View(func(s *flow.State) {
			if got := s.Slices["I1.2"].WaitsOn; !slices.Equal(got, want) {
				t.Fatalf("waits_on = %v, want %v", got, want)
			}
		})
	}
	before := len(eng.EventsAfter(0, 0))
	poll, err := p.Once(ctx)
	if err != nil || len(poll.Errors) != 0 || len(poll.Recorded) != 0 || len(eng.EventsAfter(0, 0)) != before || !strings.Contains(strings.Join(poll.Notes, "\n"), "I1.2 cannot be claimed: it waits on I1.1, whose PR acme/app#7 is closed on GitHub but not merged in Metronome; a person must decide") {
		t.Fatalf("closed poll = %+v, err = %v", poll, err)
	}
	checkWaiting([]string{"I1.1"})
	snap.State = github.PRMerged
	poll, err = p.Once(ctx)
	if err != nil || len(poll.Errors) != 0 || len(poll.Recorded) != 0 || len(eng.EventsAfter(0, 0)) != before || !strings.Contains(strings.Join(poll.Notes, "\n"), "I1.2 cannot be claimed: it waits on I1.1, whose PR acme/app#7 is merged on GitHub but not merged in Metronome; a person must decide") {
		t.Fatalf("merge without a merger poll = %+v, err = %v", poll, err)
	}
	checkWaiting([]string{"I1.1"})
	snap.State, snap.MergedBy = github.PRMerged, "pat"
	poll, err = p.Once(ctx)
	if err != nil || len(poll.Errors) != 0 || len(poll.Recorded) != 1 || poll.Recorded[0].Event != "merged" || strings.Contains(strings.Join(poll.Notes, "\n"), "cannot be claimed") {
		t.Fatalf("merged poll = %+v, err = %v", poll, err)
	}
	checkWaiting(nil)
}

func TestPollingCarriesASliceFromSubmitToMerge(t *testing.T) {
	eng, err := flow.NewEngine(context.Background(), flow.DefaultConfig(), store.NewMemory(), nil)
	if err != nil {
		t.Fatal(err)
	}
	submittedSlice(t, eng)
	gh := fakeGitHub{}
	p := github.Poller{
		Client: &api.Client{BaseURL: "http://metronome.test", Actor: "github", HTTP: &http.Client{Transport: handlerTransport{api.Handler(eng)}}},
		Source: gh,
	}
	poll := func() github.PollResult {
		t.Helper()
		res, err := p.Once(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Errors) > 0 {
			t.Fatalf("poll errors: %v", res.Errors)
		}
		return res
	}
	key := "acme/app metronome/I1.1"

	if res := poll(); len(res.Recorded) != 0 {
		t.Fatalf("recorded %v with no PR", res.Recorded)
	}

	gh[key] = &github.Snapshot{Repo: "acme/app", Number: 7, HeadSHA: "aaa", Draft: true, State: github.PROpen, HeadChecks: github.ChecksPending}
	poll()
	if sl := slice(t, eng); sl.Status != flow.SlicePROpen || sl.PR.Number != 7 {
		t.Fatalf("after the PR opened: %+v", sl)
	}

	gh[key].HeadChecks = github.ChecksSuccess
	poll()
	if sl := slice(t, eng); sl.Status != flow.SlicePROpen || !sl.PR.ChecksGreen {
		t.Fatalf("a green draft: %+v", sl)
	}

	gh[key].Draft = false
	poll()
	if sl := slice(t, eng); sl.Status != flow.SliceInReview || sl.HoldsExecute {
		t.Fatalf("ready for review: %+v", sl)
	}

	gh[key].Reviews = []github.Review{{ID: "PRR_1", Author: "rev", State: github.ReviewApproved, CommitSHA: "aaa", SubmittedAt: time.Now()}}
	poll()
	if sl := slice(t, eng); sl.Status != flow.SliceApproved {
		t.Fatalf("approved: %+v", sl)
	}

	gh[key].State = github.PRMerged
	res := poll()
	if sl := slice(t, eng); sl.Status != flow.SliceMerged {
		t.Fatalf("merged: %+v", sl)
	}
	if len(res.Recorded) != 1 || res.Recorded[0].Event != "merged" {
		t.Fatalf("last poll recorded %v", res.Recorded)
	}

	before := len(eng.EventsAfter(0, 0))
	if res := poll(); len(res.Recorded) != 0 {
		t.Fatalf("a repeat poll recorded %v", res.Recorded)
	}
	if after := len(eng.EventsAfter(0, 0)); after != before {
		t.Fatalf("a repeat poll added %d events", after-before)
	}
}

func pollingApprovedSlice(t *testing.T) (*flow.Engine, *api.Client, *github.Snapshot, func() github.PollResult) {
	t.Helper()
	eng, err := flow.NewEngine(context.Background(), flow.DefaultConfig(), store.NewMemory(), nil)
	if err != nil {
		t.Fatal(err)
	}
	submittedSlice(t, eng)
	snap := &github.Snapshot{
		Repo: "acme/app", Number: 7, HeadSHA: "aaa", State: github.PROpen, HeadChecks: github.ChecksSuccess,
		Reviews: []github.Review{{ID: "PRR_1", Author: "rev", State: github.ReviewApproved, CommitSHA: "aaa", SubmittedAt: time.Now()}},
	}
	client := &api.Client{BaseURL: "http://metronome.test", Actor: "github", HTTP: &http.Client{Transport: handlerTransport{api.Handler(eng)}}}
	p := github.Poller{Client: client, Source: fakeGitHub{"acme/app metronome/I1.1": snap}}
	poll := func() github.PollResult {
		t.Helper()
		res, err := p.Once(context.Background())
		if err != nil || len(res.Errors) > 0 {
			t.Fatalf("poll error: %v, errors: %v", err, res.Errors)
		}
		return res
	}
	poll()
	if sl := slice(t, eng); sl.Status != flow.SliceApproved || sl.PR.ApprovedSHA != "aaa" {
		t.Fatalf("approved slice = %+v, PR = %+v", sl, sl.PR)
	}
	eng.View(func(s *flow.State) {
		if it := s.Items["I1"]; it.Status != flow.ItemReleasing || !it.HoldsRelease {
			t.Fatalf("item = %+v", it)
		}
	})
	return eng, client, snap, poll
}

func wantRecorded(t *testing.T, res github.PollResult, want ...string) {
	t.Helper()
	var got []string
	for _, action := range res.Recorded {
		got = append(got, action.Event)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("recorded = %v, want %v (notes %v)", got, want, res.Notes)
	}
}

func TestAHeadThatMovesAfterApprovalWithdrawsItAndNeedsAFreshApproval(t *testing.T) {
	eng, client, snap, poll := pollingApprovedSlice(t)
	snap.HeadSHA = "bbb"
	res := poll()
	wantRecorded(t, res, "pr-opened", "checks", "ready-for-review")
	if !strings.Contains(strings.Join(res.Notes, "\n"), "withdraws the approval") {
		t.Fatalf("notes = %v", res.Notes)
	}
	if sl := slice(t, eng); sl.Status != flow.SliceInReview || sl.PR.HeadSHA != "bbb" || sl.PR.ApprovedSHA != "" || !sl.HoldsReview || sl.HoldsExecute || !sl.PR.ChecksRecorded || !sl.PR.ChecksGreen {
		t.Fatalf("withdrawn slice = %+v, PR = %+v", sl, sl.PR)
	}
	var board api.Board
	if err := client.Get(context.Background(), "/v1/board", &board); err != nil {
		t.Fatal(err)
	}
	if board.Usage.Review != 1 || board.Usage.Release != 0 || board.Usage.PerApprover["rev"] != 1 || board.Items[0].Status != flow.ItemExecuting {
		t.Fatalf("board = %+v", board)
	}
	before := len(eng.EventsAfter(0, 0))
	wantRecorded(t, poll())
	if len(eng.EventsAfter(0, 0)) != before {
		t.Fatal("repeat poll added events")
	}
	snap.Reviews = append(snap.Reviews, github.Review{ID: "PRR_2", Author: "rev", State: github.ReviewApproved, CommitSHA: "bbb", SubmittedAt: snap.Reviews[0].SubmittedAt.Add(time.Minute)})
	wantRecorded(t, poll(), "approved")
	if sl := slice(t, eng); sl.Status != flow.SliceApproved || sl.PR.ApprovedSHA != "bbb" {
		t.Fatalf("fresh approval = %+v, PR = %+v", sl, sl.PR)
	}
	eng.View(func(s *flow.State) {
		if u := s.Usage(); u.Release != 1 || u.Review != 0 || u.PerApprover["rev"] != 0 {
			t.Fatalf("usage = %+v", u)
		}
	})
	snap.State = github.PRMerged
	wantRecorded(t, poll(), "merged")
	if sl := slice(t, eng); sl.Status != flow.SliceMerged {
		t.Fatalf("merged slice = %+v", sl)
	}
}

func TestAMergeAtAHeadOtherThanTheApprovedOneRecordsTheHeadThenTheMerge(t *testing.T) {
	eng, _, snap, poll := pollingApprovedSlice(t)
	snap.State, snap.HeadSHA, snap.MergedBy = github.PRMerged, "bbb", "pat"
	res := poll()
	wantRecorded(t, res, "pr-opened", "merged")
	if res.Recorded[1].Request.By != "pat" {
		t.Fatalf("merge = %+v", res.Recorded[1])
	}
	if sl := slice(t, eng); sl.Status != flow.SliceMerged || sl.PR.HeadSHA != "bbb" || sl.PR.ApprovedSHA != "bbb" || sl.HoldsReview || sl.HoldsExecute {
		t.Fatalf("merged slice = %+v, PR = %+v", sl, sl.PR)
	}
	eng.View(func(s *flow.State) {
		if u := s.Usage(); u.Release != 1 || u.Review != 0 || u.Execute != 0 {
			t.Fatalf("usage = %+v", u)
		}
	})
	before := len(eng.EventsAfter(0, 0))
	wantRecorded(t, poll())
	if len(eng.EventsAfter(0, 0)) != before {
		t.Fatal("repeat poll added events")
	}
}

func TestAFailingLookupIsReportedAndTheRestOfThePollGoesOn(t *testing.T) {
	eng, err := flow.NewEngine(context.Background(), flow.DefaultConfig(), store.NewMemory(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r, err := eng.SyncBacklog(ctx, "pat", "", "", []flow.BacklogItem{{Ref: "A", Title: "a", Owner: "pat"}})
	must(t, r, err)
	r, err = eng.StartPlan(ctx, "agent", "", "", "")
	must(t, r, err)
	r, err = eng.ProposePlan(ctx, "agent", "", "I1", []flow.SliceSpec{{Title: "a", Repo: "acme/app"}, {Title: "b", Repo: "acme/app"}}, "")
	must(t, r, err)
	r, err = eng.ApprovePlan(ctx, "pat", "", "I1", nil)
	must(t, r, err)
	for i, branch := range []string{"metronome/broken", "metronome/I1.2"} {
		r, err = eng.Claim(ctx, "laptop", "", "laptop", "", "")
		must(t, r, err)
		r, err = eng.Submit(ctx, "laptop", "", []string{"R1", "R2"}[i], branch)
		must(t, r, err)
	}
	gh := fakeGitHub{"acme/app metronome/I1.2": {Repo: "acme/app", Number: 8, HeadSHA: "bbb", State: github.PROpen, HeadChecks: github.ChecksPending}}
	p := github.Poller{
		Client: &api.Client{BaseURL: "http://metronome.test", Actor: "github", HTTP: &http.Client{Transport: handlerTransport{api.Handler(eng)}}},
		Source: gh,
	}
	res, err := p.Once(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "not signed in") {
		t.Fatalf("errors = %v", res.Errors)
	}
	if len(res.Recorded) != 1 || res.Recorded[0].Request.SliceID != "I1.2" {
		t.Fatalf("recorded = %v", res.Recorded)
	}
}

func TestAResubmitOnTheSameHeadAfterFailedChecksStillReachesReview(t *testing.T) {
	eng, err := flow.NewEngine(context.Background(), flow.DefaultConfig(), store.NewMemory(), nil)
	if err != nil {
		t.Fatal(err)
	}
	submittedSlice(t, eng)
	key := "acme/app metronome/I1.1"
	gh := fakeGitHub{key: {Repo: "acme/app", Number: 7, HeadSHA: "aaa", State: github.PROpen, HeadChecks: github.ChecksFailure}}
	p := github.Poller{
		Client: &api.Client{BaseURL: "http://metronome.test", Actor: "github", HTTP: &http.Client{Transport: handlerTransport{api.Handler(eng)}}},
		Source: gh,
	}
	if _, err := p.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sl := slice(t, eng); sl.Status != flow.SliceStalled || !sl.Rework {
		t.Fatalf("after failed checks: %+v", sl)
	}

	ctx := context.Background()
	r, err := eng.Claim(ctx, "laptop", "", "laptop", "", "")
	must(t, r, err)
	r, err = eng.Submit(ctx, "laptop", "", "R2", "metronome/I1.1")
	must(t, r, err)
	gh[key].HeadChecks = github.ChecksSuccess
	res, err := p.Once(ctx)
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("poll: %v %v", err, res.Errors)
	}
	if sl := slice(t, eng); sl.Status != flow.SliceInReview {
		t.Fatalf("after the resubmit and a green rerun: %+v (recorded %v, notes %v)", sl, res.Recorded, res.Notes)
	}
}

func TestAHeadPushedAwayAndBackInReviewCanStillBeApproved(t *testing.T) {
	eng, err := flow.NewEngine(context.Background(), flow.DefaultConfig(), store.NewMemory(), nil)
	if err != nil {
		t.Fatal(err)
	}
	submittedSlice(t, eng)
	key := "acme/app metronome/I1.1"
	gh := fakeGitHub{key: {Repo: "acme/app", Number: 7, HeadSHA: "aaa", State: github.PROpen, HeadChecks: github.ChecksSuccess}}
	p := github.Poller{
		Client: &api.Client{BaseURL: "http://metronome.test", Actor: "github", HTTP: &http.Client{Transport: handlerTransport{api.Handler(eng)}}},
		Source: gh,
	}
	poll := func() github.PollResult {
		t.Helper()
		res, err := p.Once(context.Background())
		if err != nil || len(res.Errors) > 0 {
			t.Fatalf("poll: %v %v", err, res.Errors)
		}
		return res
	}
	poll()
	gh[key].HeadSHA = "bbb"
	poll()
	gh[key].HeadSHA = "aaa"
	gh[key].Reviews = []github.Review{{ID: "PRR_1", Author: "rev", State: github.ReviewApproved, CommitSHA: "aaa", SubmittedAt: time.Now()}}
	res := poll()
	if sl := slice(t, eng); sl.Status != flow.SliceApproved || sl.PR.HeadSHA != "aaa" {
		t.Fatalf("after the head came back and was approved: %+v (recorded %v, notes %v)", sl, res.Recorded, res.Notes)
	}
}

func TestASliceInReviewWhoseHeadTurnsRedGoesBackForReworkAndReturnsWhenGreen(t *testing.T) {
	ctx := context.Background()
	eng, err := flow.NewEngine(ctx, flow.DefaultConfig(), store.NewMemory(), nil)
	if err != nil {
		t.Fatal(err)
	}
	submittedSlice(t, eng)
	key := "acme/app metronome/I1.1"
	gh := fakeGitHub{key: {Repo: "acme/app", Number: 7, HeadSHA: "aaa", State: github.PROpen, HeadChecks: github.ChecksSuccess}}
	p := github.Poller{
		Client: &api.Client{BaseURL: "http://metronome.test", Actor: "github", HTTP: &http.Client{Transport: handlerTransport{api.Handler(eng)}}},
		Source: gh,
	}
	poll := func() github.PollResult {
		t.Helper()
		res, err := p.Once(ctx)
		if err != nil || len(res.Errors) > 0 {
			t.Fatalf("poll: %v %v", err, res.Errors)
		}
		return res
	}
	poll()
	if sl := slice(t, eng); sl.Status != flow.SliceInReview {
		t.Fatalf("slice before red checks = %+v", sl)
	}
	gh[key].HeadSHA, gh[key].HeadChecks = "bbb", github.ChecksFailure
	poll()
	if sl := slice(t, eng); sl.Status != flow.SliceStalled || !sl.Rework || !sl.HoldsReview || sl.HoldsExecute || sl.PR.HeadSHA != "bbb" {
		t.Fatalf("slice after red checks = %+v", sl)
	}
	var board api.Board
	if err := p.Client.Get(ctx, "/v1/board", &board); err != nil {
		t.Fatal(err)
	}
	if u := board.Usage; u.Execute != 0 || u.Review != 1 || u.PerApprover["rev"] != 0 {
		t.Fatalf("usage after red checks = %+v", u)
	}
	if res := poll(); len(res.Recorded) != 0 {
		t.Fatalf("poll recorded stalled slice: %v", res.Recorded)
	}
	r, err := eng.Claim(ctx, "laptop", "", "laptop", "", "")
	must(t, r, err)
	r, err = eng.Submit(ctx, "laptop", "", "R2", "metronome/I1.1")
	must(t, r, err)
	gh[key].HeadSHA, gh[key].HeadChecks = "ccc", github.ChecksSuccess
	poll()
	if sl := slice(t, eng); sl.Status != flow.SliceInReview || sl.PR.HeadSHA != "ccc" || sl.Rework || !sl.HoldsReview || sl.HoldsExecute {
		t.Fatalf("slice after green checks on fixed head = %+v", sl)
	}
	board = api.Board{}
	if err := p.Client.Get(ctx, "/v1/board", &board); err != nil {
		t.Fatal(err)
	}
	if u := board.Usage; u.Execute != 0 || u.Review != 1 || u.PerApprover["rev"] != 1 {
		t.Fatalf("usage back in review = %+v", u)
	}
}

func TestAMergeBeforeApprovalIsRecordedAsApprovedByTheMerger(t *testing.T) {
	eng, err := flow.NewEngine(context.Background(), flow.DefaultConfig(), store.NewMemory(), nil)
	if err != nil {
		t.Fatal(err)
	}
	submittedSlice(t, eng)
	key := "acme/app metronome/I1.1"
	gh := fakeGitHub{key: {Repo: "acme/app", Number: 7, HeadSHA: "aaa", State: github.PROpen, HeadChecks: github.ChecksPending}}
	p := github.Poller{
		Client: &api.Client{BaseURL: "http://metronome.test", Actor: "github", HTTP: &http.Client{Transport: handlerTransport{api.Handler(eng)}}},
		Source: gh,
	}
	if _, err := p.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	gh[key].State, gh[key].MergedBy = github.PRMerged, "pat"
	res, err := p.Once(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sl := slice(t, eng)
	if sl.Status != flow.SliceMerged || sl.PR.ApprovedSHA != "aaa" || sl.HoldsExecute || sl.HoldsReview {
		t.Fatalf("after a merge before approval: %+v (notes %v)", sl, res.Notes)
	}
	if len(res.Recorded) != 1 || res.Recorded[0].String() != "I1.1 merged by pat, who approves it" {
		t.Fatalf("recorded = %v", res.Recorded)
	}
}

func pollWithoutWrites(t *testing.T, eng *flow.Engine, transport *countingTransport, poll func() github.PollResult) github.PollResult {
	t.Helper()
	before := len(eng.EventsAfter(0, 0))
	posts := transport.posts
	res := poll()
	if len(res.Recorded) != 0 || transport.posts != posts || len(eng.EventsAfter(0, 0)) != before {
		t.Fatalf("poll recorded %v, made %d POSTs and added %d events", res.Recorded, transport.posts-posts, len(eng.EventsAfter(0, 0))-before)
	}
	return res
}

func assertReworkPRNote(t *testing.T, res github.PollResult, outcome string, status flow.SliceStatus) {
	t.Helper()
	if len(res.Notes) != 1 {
		t.Fatalf("notes = %v, want one note", res.Notes)
	}
	for _, want := range []string{"I1.1", "acme/app#7", outcome, string(status) + " for rework", "a person must decide"} {
		if !strings.Contains(res.Notes[0], want) {
			t.Errorf("note %q does not mention %q", res.Notes[0], want)
		}
	}
}

func TestAMergeOfAStalledSlicesPRIsReportedAndNotRecorded(t *testing.T) {
	ctx := context.Background()
	eng, err := flow.NewEngine(ctx, flow.DefaultConfig(), store.NewMemory(), nil)
	if err != nil {
		t.Fatal(err)
	}
	submittedSlice(t, eng)
	key := "acme/app metronome/I1.1"
	gh := fakeGitHub{key: {Repo: "acme/app", Number: 7, HeadSHA: "aaa", State: github.PROpen, HeadChecks: github.ChecksFailure}}
	transport := &countingTransport{handlerTransport: handlerTransport{api.Handler(eng)}}
	p := github.Poller{
		Client: &api.Client{BaseURL: "http://metronome.test", Actor: "github", HTTP: &http.Client{Transport: transport}},
		Source: gh,
	}
	poll := func() github.PollResult {
		t.Helper()
		res, err := p.Once(ctx)
		if err != nil || len(res.Errors) > 0 {
			t.Fatalf("poll: %v %v", err, res.Errors)
		}
		return res
	}
	poll()
	if sl := slice(t, eng); sl.Status != flow.SliceStalled || !sl.Rework || sl.PR == nil || sl.PR.HeadSHA != "aaa" {
		t.Fatalf("slice after red checks = %+v", sl)
	}
	gh[key].HeadSHA, gh[key].HeadChecks = "bbb", github.ChecksSuccess
	gh[key].Reviews = []github.Review{{ID: "PRR_1", Author: "rev", State: github.ReviewApproved, CommitSHA: "bbb", SubmittedAt: time.Now()}}
	if res := pollWithoutWrites(t, eng, transport, poll); len(res.Notes) != 0 {
		t.Fatalf("notes for open stalled PR = %v", res.Notes)
	}
	if sl := slice(t, eng); sl.Status != flow.SliceStalled || sl.PR.HeadSHA != "aaa" || sl.PR.ChecksGreen {
		t.Fatalf("slice with a new green head = %+v", sl)
	}
	gh[key].State, gh[key].MergedBy = github.PRMerged, "pat"
	res := pollWithoutWrites(t, eng, transport, poll)
	assertReworkPRNote(t, res, "merged by pat", flow.SliceStalled)
	if sl := slice(t, eng); sl.Status != flow.SliceStalled || sl.PR.HeadSHA != "aaa" || !sl.Rework {
		t.Fatalf("slice after the merge = %+v", sl)
	}
	res = pollWithoutWrites(t, eng, transport, poll)
	assertReworkPRNote(t, res, "merged by pat", flow.SliceStalled)
}

func TestACloseOfAStalledSlicesPRIsReported(t *testing.T) {
	ctx := context.Background()
	eng, err := flow.NewEngine(ctx, flow.DefaultConfig(), store.NewMemory(), nil)
	if err != nil {
		t.Fatal(err)
	}
	submittedSlice(t, eng)
	key := "acme/app metronome/I1.1"
	gh := fakeGitHub{key: {Repo: "acme/app", Number: 7, HeadSHA: "aaa", State: github.PROpen, HeadChecks: github.ChecksFailure}}
	transport := &countingTransport{handlerTransport: handlerTransport{api.Handler(eng)}}
	p := github.Poller{
		Client: &api.Client{BaseURL: "http://metronome.test", Actor: "github", HTTP: &http.Client{Transport: transport}},
		Source: gh,
	}
	poll := func() github.PollResult {
		t.Helper()
		res, err := p.Once(ctx)
		if err != nil || len(res.Errors) > 0 {
			t.Fatalf("poll: %v %v", err, res.Errors)
		}
		return res
	}
	poll()
	if sl := slice(t, eng); sl.Status != flow.SliceStalled || !sl.Rework || sl.PR == nil {
		t.Fatalf("slice after red checks = %+v", sl)
	}
	r, err := eng.Claim(ctx, "laptop", "", "laptop", "", "")
	must(t, r, err)
	if sl := slice(t, eng); sl.Status != flow.SliceRunning {
		t.Fatalf("slice after claiming rework = %+v", sl)
	}
	if res := pollWithoutWrites(t, eng, transport, poll); len(res.Notes) != 0 {
		t.Fatalf("notes for running slice = %v", res.Notes)
	}
	r, err = eng.ReleaseRun(ctx, "laptop", "", "R2")
	must(t, r, err)
	if sl := slice(t, eng); sl.Status != flow.SliceStalled || sl.PR == nil || sl.PR.Number != 7 {
		t.Fatalf("slice after releasing rework = %+v", sl)
	}
	gh[key].State = github.PRClosed
	res := pollWithoutWrites(t, eng, transport, poll)
	assertReworkPRNote(t, res, "closed without merging", flow.SliceStalled)
	if sl := slice(t, eng); sl.Status != flow.SliceStalled || sl.PR.HeadSHA != "aaa" {
		t.Fatalf("slice after the close = %+v", sl)
	}
	res = pollWithoutWrites(t, eng, transport, poll)
	assertReworkPRNote(t, res, "closed without merging", flow.SliceStalled)
}

func TestMergesAndClosesOfAQueuedRunningOrFlaggedSlicesPRAreReportedAndNotRecorded(t *testing.T) {
	ctx := context.Background()
	eng, err := flow.NewEngine(ctx, flow.DefaultConfig(), store.NewMemory(), nil)
	if err != nil {
		t.Fatal(err)
	}
	submittedSlice(t, eng)
	snap := &github.Snapshot{
		Repo: "acme/app", Number: 7, HeadSHA: "aaa", State: github.PROpen, HeadChecks: github.ChecksSuccess,
		Reviews: []github.Review{{ID: "PRR_1", Author: "rev", State: github.ReviewChangesRequested, CommitSHA: "aaa", SubmittedAt: time.Now()}},
	}
	transport := &countingTransport{handlerTransport: handlerTransport{api.Handler(eng)}}
	p := github.Poller{
		Client: &api.Client{BaseURL: "http://metronome.test", Actor: "github", HTTP: &http.Client{Transport: transport}},
		Source: fakeGitHub{"acme/app metronome/I1.1": snap},
	}
	poll := func() github.PollResult {
		t.Helper()
		res, err := p.Once(ctx)
		if err != nil || len(res.Errors) > 0 {
			t.Fatalf("poll: %v %v", err, res.Errors)
		}
		return res
	}
	assertSlice := func(status flow.SliceStatus) {
		t.Helper()
		if sl := slice(t, eng); sl.Status != status || !sl.Rework || sl.PR == nil || sl.PR.Number != 7 || sl.PR.HeadSHA != "aaa" {
			t.Fatalf("slice = %+v, PR = %+v, want %s with the original PR", sl, sl.PR, status)
		}
	}
	assertQuietPoll := func(status flow.SliceStatus) {
		t.Helper()
		if res := pollWithoutWrites(t, eng, transport, poll); len(res.Notes) != 0 {
			t.Fatalf("notes for open %s PR = %v", status, res.Notes)
		}
		assertSlice(status)
	}

	poll()
	assertSlice(flow.SliceQueued)
	snap.HeadSHA = "bbb"
	snap.Reviews = append(snap.Reviews, github.Review{ID: "PRR_2", Author: "rev", State: github.ReviewApproved, CommitSHA: "bbb", SubmittedAt: snap.Reviews[0].SubmittedAt.Add(time.Minute)})
	assertQuietPoll(flow.SliceQueued)
	snap.State, snap.MergedBy = github.PRMerged, "pat"
	res := pollWithoutWrites(t, eng, transport, poll)
	assertReworkPRNote(t, res, "merged by pat", flow.SliceQueued)
	assertSlice(flow.SliceQueued)
	if repeat := pollWithoutWrites(t, eng, transport, poll); !slices.Equal(repeat.Notes, res.Notes) {
		t.Fatalf("repeat notes = %v, want %v", repeat.Notes, res.Notes)
	}

	snap.State, snap.MergedBy = github.PROpen, ""
	r, err := eng.Claim(ctx, "laptop", "", "laptop", "", "")
	must(t, r, err)
	assertQuietPoll(flow.SliceRunning)
	snap.State = github.PRClosed
	res = pollWithoutWrites(t, eng, transport, poll)
	assertReworkPRNote(t, res, "closed without merging", flow.SliceRunning)
	assertSlice(flow.SliceRunning)

	snap.State = github.PROpen
	r, err = eng.FlagRun(ctx, "laptop", "", "R2", flow.AskAnswer, "CSV or JSON?", "")
	must(t, r, err)
	assertQuietPoll(flow.SliceFlagged)
	snap.State = github.PRMerged
	res = pollWithoutWrites(t, eng, transport, poll)
	assertReworkPRNote(t, res, "who merged", flow.SliceFlagged)
	assertSlice(flow.SliceFlagged)
}
