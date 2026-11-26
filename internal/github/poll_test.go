package github_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
	r, err := eng.SyncBacklog(ctx, "pat", "", []flow.BacklogItem{{Ref: "ACME-1", Title: "Add export", Owner: "pat", PredictedTier: 1}})
	must(t, r, err)
	r, err = eng.StartPlan(ctx, "agent", "", "")
	must(t, r, err)
	r, err = eng.ProposePlan(ctx, "agent", "", "I1", []flow.SliceSpec{{Title: "API endpoint", Repo: "acme/app", Approvers: []string{"rev"}}}, "")
	must(t, r, err)
	r, err = eng.ApprovePlan(ctx, "pat", "", "I1", nil)
	must(t, r, err)
	r, err = eng.Claim(ctx, "laptop", "", "laptop", "")
	must(t, r, err)
	r, err = eng.Submit(ctx, "laptop", "", "R1", "metronome/I1.1")
	must(t, r, err)
}

func slice(t *testing.T, eng *flow.Engine) flow.Slice {
	var sl flow.Slice
	eng.View(func(s *flow.State) { sl = *s.Slices["I1.1"] })
	return sl
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

func TestAFailingLookupIsReportedAndTheRestOfThePollGoesOn(t *testing.T) {
	eng, err := flow.NewEngine(context.Background(), flow.DefaultConfig(), store.NewMemory(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r, err := eng.SyncBacklog(ctx, "pat", "", []flow.BacklogItem{{Ref: "A", Title: "a", Owner: "pat"}})
	must(t, r, err)
	r, err = eng.StartPlan(ctx, "agent", "", "")
	must(t, r, err)
	r, err = eng.ProposePlan(ctx, "agent", "", "I1", []flow.SliceSpec{{Title: "a", Repo: "acme/app"}, {Title: "b", Repo: "acme/app"}}, "")
	must(t, r, err)
	r, err = eng.ApprovePlan(ctx, "pat", "", "I1", nil)
	must(t, r, err)
	for i, branch := range []string{"metronome/broken", "metronome/I1.2"} {
		r, err = eng.Claim(ctx, "laptop", "", "laptop", "")
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
	r, err := eng.Claim(ctx, "laptop", "", "laptop", "")
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
