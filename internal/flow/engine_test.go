package flow_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/store"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }
func newClock() *clock                   { return &clock{now: time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)} }
func ctx() context.Context               { return context.Background() }
func intp(i int) *int                    { return &i }

type harness struct {
	t     *testing.T
	clock *clock
	store *store.Memory
	eng   *flow.Engine
	cfg   flow.Config
}

func newHarness(t *testing.T, tweak func(*flow.Config)) *harness {
	t.Helper()
	cfg := flow.DefaultConfig()
	if tweak != nil {
		tweak(&cfg)
	}
	h := &harness{t: t, clock: newClock(), store: store.NewMemory(), cfg: cfg}
	h.eng = h.reload()
	return h
}

func (h *harness) reload() *flow.Engine {
	h.t.Helper()
	eng, err := flow.NewEngine(ctx(), h.cfg, h.store, h.clock.Now)
	if err != nil {
		h.t.Fatal(err)
	}
	return eng
}

func (h *harness) ok(r flow.Result, err error) flow.Result {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

func wantCode(t *testing.T, err error, code flow.ErrorCode) {
	t.Helper()
	if flow.CodeOf(err) != code {
		t.Fatalf("got error %v, want code %s", err, code)
	}
}

func (h *harness) backlog(n int) {
	h.t.Helper()
	var items []flow.BacklogItem
	for i := 1; i <= n; i++ {
		items = append(items, flow.BacklogItem{Ref: fmt.Sprintf("ACME-%d", i), Title: fmt.Sprintf("item %d", i), Owner: "pat", PredictedTier: 2})
	}
	h.ok(h.eng.SyncBacklog(ctx(), "test", "", items))
}

func (h *harness) approvedItem(itemID string, slices int) {
	h.t.Helper()
	h.approveItem(itemID, slices, nil)
}

func (h *harness) approvedItemAt(itemID string, slices, tier int) {
	h.t.Helper()
	h.approveItem(itemID, slices, intp(tier))
}

func (h *harness) approveItem(itemID string, slices int, tier *int) {
	h.t.Helper()
	h.ok(h.eng.StartPlan(ctx(), "test", "", itemID))
	var specs []flow.SliceSpec
	for i := 1; i <= slices; i++ {
		specs = append(specs, flow.SliceSpec{Title: fmt.Sprintf("slice %d", i), Repo: "acme/app", Approvers: []string{"rev"}})
	}
	h.ok(h.eng.ProposePlan(ctx(), "test", "", itemID, specs, ""))
	h.ok(h.eng.ApprovePlan(ctx(), "pat", "", itemID, tier))
}

func (h *harness) claim(runner string) flow.Claim {
	h.t.Helper()
	r := h.ok(h.eng.Claim(ctx(), runner, "", runner, ""))
	c, ok := flow.ClaimFrom(r)
	if !ok {
		h.t.Fatal("claim produced no slice_claimed event")
	}
	return c
}

func (h *harness) slice(id string) flow.Slice {
	var out flow.Slice
	h.eng.View(func(s *flow.State) { out = *s.Slices[id] })
	return out
}

func (h *harness) history(id string) flow.SliceHistory {
	var out flow.SliceHistory
	h.eng.View(func(s *flow.State) { out = s.Runs[id].History })
	return out
}

func (h *harness) item(id string) flow.Item {
	var out flow.Item
	h.eng.View(func(s *flow.State) { out = *s.Items[id] })
	return out
}

func (h *harness) usage() flow.Usage {
	var out flow.Usage
	h.eng.View(func(s *flow.State) { out = s.Usage() })
	return out
}

func (h *harness) toReview(sliceID string) {
	h.t.Helper()
	sl := h.slice(sliceID)
	h.ok(h.eng.Submit(ctx(), "runner", "", sl.RunID, "metronome/"+sliceID))
	h.ok(h.eng.PROpened(ctx(), "github", "", sliceID, "acme/app", 10, "sha-"+sliceID))
	h.ok(h.eng.ChecksCompleted(ctx(), "github", "", sliceID, "sha-"+sliceID, true))
	h.ok(h.eng.ReadyForReview(ctx(), "github", "", sliceID))
}

func (h *harness) newHead(sliceID, sha string) {
	h.t.Helper()
	h.ok(h.eng.PROpened(ctx(), "github", "", sliceID, "acme/app", 10, sha))
}

func (h *harness) reapprove(sliceID, sha string) {
	h.t.Helper()
	h.ok(h.eng.ChecksCompleted(ctx(), "github", "", sliceID, sha, true))
	h.ok(h.eng.ReadyForReview(ctx(), "github", "", sliceID))
	h.ok(h.eng.PRApproved(ctx(), "github", "", sliceID, sha, "rev"))
}

func (h *harness) claimAndApprove() string {
	h.t.Helper()
	id := h.claim("laptop").SliceID
	h.toReview(id)
	h.ok(h.eng.PRApproved(ctx(), "github", "", id, "sha-"+id, "rev"))
	return id
}

func (h *harness) wantSlots(review, release int) {
	h.t.Helper()
	if u := h.usage(); u.Review != review || u.Release != release {
		h.t.Fatalf("usage = %+v, want Review %d, Release %d", u, review, release)
	}
}

func TestBacklogRefillsToTheReadyLimitOnlyBelowTheThreshold(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(14)
	if got := h.usage().Ready; got != 10 {
		t.Fatalf("ready = %d, want 10", got)
	}
	for _, id := range []string{"I1", "I2", "I3"} {
		h.ok(h.eng.StartPlan(ctx(), "test", "", id))
	}
	h.backlog(14)
	if got := h.usage().Ready; got != 7 {
		t.Fatalf("ready = %d after refill at 7, want 7 (no refill at or above 6)", got)
	}
	h.ok(h.eng.DropPlan(ctx(), "pat", "", "I1"))
	h.eng.View(func(s *flow.State) {
		for _, it := range s.Items {
			if it.Ref == "ACME-1" && it.Status != flow.ItemDropped {
				t.Fatalf("ACME-1 is %s", it.Status)
			}
		}
	})
}

func TestPlanLimitAndApprovalFlag(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(5)
	for i := 0; i < 3; i++ {
		h.ok(h.eng.StartPlan(ctx(), "test", "", ""))
	}
	_, err := h.eng.StartPlan(ctx(), "test", "", "")
	wantCode(t, err, flow.CodeNoCapacity)

	h.ok(h.eng.ProposePlan(ctx(), "agent", "", "I1", []flow.SliceSpec{{Title: "a", Repo: "acme/app"}}, "lead"))
	var inbox []*flow.Flag
	h.eng.View(func(s *flow.State) { inbox = s.Inbox("lead") })
	if len(inbox) != 1 || inbox[0].Ask != flow.AskApprove {
		t.Fatalf("lead inbox = %+v, want one approve ask", inbox)
	}
	_, err = h.eng.ResolveFlag(ctx(), "lead", "", inbox[0].ID, "ok")
	wantCode(t, err, flow.CodeConflict)

	h.ok(h.eng.ApprovePlan(ctx(), "lead", "", "I1", intp(2)))
	it := h.item("I1")
	if it.Status != flow.ItemExecuting || it.HoldsPlan || it.PredictedTier != 2 || len(it.Slices) != 1 {
		t.Fatalf("item after approval = %+v", it)
	}
	h.eng.View(func(s *flow.State) { inbox = s.Inbox("lead") })
	if len(inbox) != 0 {
		t.Fatalf("approval left %d open asks", len(inbox))
	}
	h.ok(h.eng.StartPlan(ctx(), "test", "", ""))
}

func TestClaimReservesReviewSoReviewBoundsTheWholePipeline(t *testing.T) {
	h := newHarness(t, func(c *flow.Config) { c.Limits.Execute = 5; c.Limits.Review = 2 })
	h.backlog(1)
	h.approvedItem("I1", 3)
	a := h.claim("r1")
	h.claim("r2")
	_, err := h.eng.Claim(ctx(), "r3", "", "r3", "")
	wantCode(t, err, flow.CodeNoCapacity)

	h.toReview(a.SliceID)
	if u := h.usage(); u.Execute != 1 || u.Review != 2 {
		t.Fatalf("usage after one PR into review = %+v, want execute 1 review 2", u)
	}
	_, err = h.eng.Claim(ctx(), "r3", "", "r3", "")
	wantCode(t, err, flow.CodeNoCapacity)
}

func TestExecuteLimitBindsWhenReviewHasRoom(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItem("I1", 6)
	for i := 1; i <= 5; i++ {
		h.claim(fmt.Sprintf("r%d", i))
	}
	_, err := h.eng.Claim(ctx(), "r6", "", "r6", "")
	wantCode(t, err, flow.CodeNoCapacity)
}

func TestLeaseExpiryStallsThenStallTimeoutParks(t *testing.T) {
	h := newHarness(t, func(c *flow.Config) { c.Limits.Execute = 1 })
	h.backlog(1)
	h.approvedItem("I1", 2)
	c := h.claim("laptop")

	h.clock.advance(h.cfg.Lease)
	h.ok(h.eng.Tick(ctx()))
	sl := h.slice(c.SliceID)
	if sl.Status != flow.SliceStalled || !sl.HoldsExecute || !sl.HoldsReview {
		t.Fatalf("after lease expiry slice = %+v, want stalled holding both slots", sl)
	}
	_, err := h.eng.Heartbeat(ctx(), "laptop", "", c.RunID, "late")
	wantCode(t, err, flow.CodeConflict)

	c2 := h.claim("cloud")
	if c2.SliceID != c.SliceID {
		t.Fatalf("cloud claimed %s, want the stalled slice %s", c2.SliceID, c.SliceID)
	}
	if history := h.history(c2.RunID); history.Reason != flow.ResumeLeaseExpired || history.PreviousRun != c.RunID {
		t.Fatalf("history after lease expiry = %+v", history)
	}
	h.clock.advance(h.cfg.Lease + h.cfg.StallTimeout)
	h.ok(h.eng.Tick(ctx()))
	sl = h.slice(c.SliceID)
	if sl.Status != flow.SliceQueued || sl.HoldsExecute || sl.HoldsReview || !sl.Front {
		t.Fatalf("after stall timeout slice = %+v, want parked at the front with no slots", sl)
	}
	if u := h.usage(); u.Execute != 0 || u.Review != 0 {
		t.Fatalf("usage after park = %+v", u)
	}
	if again := h.claim("cloud"); again.SliceID != c.SliceID {
		t.Fatalf("next claim = %s, want parked slice %s at the front", again.SliceID, c.SliceID)
	} else if history := h.history(again.RunID); history.Reason != flow.ResumeLeaseExpired || history.PreviousRun != c2.RunID {
		t.Fatalf("history after park = %+v", history)
	}
}

func TestHeartbeatExtendsTheLease(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItem("I1", 1)
	c := h.claim("laptop")
	h.clock.advance(h.cfg.Lease - time.Minute)
	h.ok(h.eng.Heartbeat(ctx(), "laptop", "", c.RunID, "halfway"))
	h.clock.advance(h.cfg.Lease - time.Minute)
	h.ok(h.eng.Tick(ctx()))
	if sl := h.slice(c.SliceID); sl.Status != flow.SliceRunning {
		t.Fatalf("slice = %s, want running", sl.Status)
	}
}

func TestFlaggedSliceKeepsSlotsAndAnswerMakesItResumable(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItem("I1", 1)
	c := h.claim("laptop")
	h.ok(h.eng.FlagRun(ctx(), "laptop", "", c.RunID, flow.AskAnswer, "Which endpoint?", ""))
	sl := h.slice(c.SliceID)
	if sl.Status != flow.SliceFlagged || !sl.HoldsExecute || !sl.HoldsReview || sl.RunID != "" {
		t.Fatalf("flagged slice = %+v", sl)
	}
	var inbox []*flow.Flag
	h.eng.View(func(s *flow.State) { inbox = s.Inbox("pat") })
	if len(inbox) != 1 {
		t.Fatalf("owner inbox has %d asks, want 1", len(inbox))
	}
	h.ok(h.eng.ResolveFlag(ctx(), "pat", "", inbox[0].ID, "the v2 one"))
	if sl := h.slice(c.SliceID); sl.Status != flow.SliceStalled || !sl.HoldsExecute {
		t.Fatalf("answered slice = %+v, want stalled holding its slots", sl)
	}
}

func TestUnansweredRunnerAskParksTheSlice(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItem("I1", 1)
	c := h.claim("laptop")
	h.ok(h.eng.FlagRun(ctx(), "laptop", "", c.RunID, flow.AskAnswer, "Which endpoint?", ""))
	h.clock.advance(h.cfg.AskTimeout)
	h.ok(h.eng.Tick(ctx()))
	sl := h.slice(c.SliceID)
	if sl.Status != flow.SliceQueued || sl.HoldsExecute || sl.HoldsReview {
		t.Fatalf("slice after ask timeout = %+v, want parked", sl)
	}
	history := h.history(h.claim("cloud").RunID)
	if history.Reason != flow.ResumeAskTimedOut || history.PreviousRun != c.RunID || history.Asks == nil || len(history.Asks) != 0 {
		t.Fatalf("history after ask timeout = %+v", history)
	}
}

func TestUnansweredPlanQuestionParksTheItemButApprovalAsksWait(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(2)
	h.ok(h.eng.StartPlan(ctx(), "test", "", "I1"))
	h.ok(h.eng.RaiseFlag(ctx(), "agent", "", flow.Subject{Kind: flow.SubjectItem, ID: "I1"}, flow.AskAnswer, "Which tenant?", ""))
	h.ok(h.eng.StartPlan(ctx(), "test", "", "I2"))
	h.ok(h.eng.ProposePlan(ctx(), "agent", "", "I2", []flow.SliceSpec{{Title: "a", Repo: "acme/app"}}, ""))

	h.clock.advance(h.cfg.AskTimeout)
	h.ok(h.eng.Tick(ctx()))
	if it := h.item("I1"); it.Status != flow.ItemReady || it.HoldsPlan || !it.Front {
		t.Fatalf("I1 = %+v, want parked back to the front of ready", it)
	}
	if it := h.item("I2"); it.Status != flow.ItemPlanning || !it.HoldsPlan {
		t.Fatalf("I2 = %+v, want still planning, waiting on approval", it)
	}
	if u := h.usage(); u.Plan != 1 {
		t.Fatalf("plan usage = %d, want 1", u.Plan)
	}
}

func TestFailedChecksSendTheSliceBackAsReworkKeepingSlots(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItem("I1", 2)
	c := h.claim("cloud")
	h.ok(h.eng.Submit(ctx(), "cloud", "", c.RunID, "metronome/"+c.SliceID))
	h.ok(h.eng.PROpened(ctx(), "github", "", c.SliceID, "acme/app", 7, "abc"))
	h.ok(h.eng.ChecksCompleted(ctx(), "github", "", c.SliceID, "abc", false))
	sl := h.slice(c.SliceID)
	if sl.Status != flow.SliceStalled || !sl.Rework || !sl.HoldsExecute || !sl.HoldsReview {
		t.Fatalf("slice after red checks = %+v", sl)
	}
	if next := h.claim("cloud"); next.SliceID != c.SliceID {
		t.Fatalf("next claim = %s, want rework %s ahead of new work", next.SliceID, c.SliceID)
	}
}

func TestFailedChecksInReviewSendTheSliceBackAsReworkKeepingTheReviewReservation(t *testing.T) {
	h := newHarness(t, func(cfg *flow.Config) { cfg.Limits.Review = 1 })
	h.backlog(1)
	h.approvedItem("I1", 2)
	c := h.claim("laptop")
	h.toReview(c.SliceID)
	if u := h.usage(); u.Execute != 0 || u.Review != 1 || u.PerApprover["rev"] != 1 {
		t.Fatalf("usage in review = %+v", u)
	}
	h.clock.advance(time.Minute)
	h.ok(h.eng.ChecksCompleted(ctx(), "github", "", c.SliceID, "sha-"+c.SliceID, false))
	if sl := h.slice(c.SliceID); sl.Status != flow.SliceStalled || !sl.Rework || sl.HoldsExecute || !sl.HoldsReview || !sl.StalledAt.Equal(h.clock.Now()) {
		t.Fatalf("slice after red checks in review = %+v", sl)
	}
	if u := h.usage(); u.Execute != 0 || u.Review != 1 || u.PerApprover["rev"] != 0 {
		t.Fatalf("usage after red checks = %+v", u)
	}
	fixer := h.claim("laptop")
	if fixer.SliceID != c.SliceID {
		t.Fatalf("next claim = %s, want rework %s", fixer.SliceID, c.SliceID)
	}
	if u := h.usage(); u.Execute != 1 || u.Review != 1 || u.PerApprover["rev"] != 0 {
		t.Fatalf("usage after rework claim = %+v", u)
	}
	h.ok(h.eng.Submit(ctx(), "laptop", "", fixer.RunID, "metronome/"+fixer.SliceID))
	h.ok(h.eng.PROpened(ctx(), "github", "", fixer.SliceID, "acme/app", 10, "sha-fix"))
	h.ok(h.eng.ChecksCompleted(ctx(), "github", "", fixer.SliceID, "sha-fix", true))
	h.ok(h.eng.ReadyForReview(ctx(), "github", "", fixer.SliceID))
	if sl := h.slice(fixer.SliceID); sl.Status != flow.SliceInReview || sl.Rework || sl.HoldsExecute || !sl.HoldsReview {
		t.Fatalf("slice after the fix = %+v", sl)
	}
	if u := h.usage(); u.Execute != 0 || u.Review != 1 || u.PerApprover["rev"] != 1 {
		t.Fatalf("usage back in review = %+v", u)
	}
}

func TestAReworkClaimAfterRedChecksInReviewNeedsAnExecuteSlot(t *testing.T) {
	h := newHarness(t, func(cfg *flow.Config) { cfg.Limits.Execute = 1 })
	h.backlog(1)
	h.approvedItem("I1", 2)
	c := h.claim("laptop")
	h.toReview(c.SliceID)
	h.claim("cloud")
	h.ok(h.eng.ChecksCompleted(ctx(), "github", "", c.SliceID, "sha-"+c.SliceID, false))
	_, err := h.eng.Claim(ctx(), "laptop", "", "laptop", c.SliceID)
	wantCode(t, err, flow.CodeNoCapacity)
	if sl := h.slice(c.SliceID); sl.Status != flow.SliceStalled || sl.HoldsExecute || !sl.HoldsReview {
		t.Fatalf("slice after refused claim = %+v", sl)
	}
	if u := h.usage(); u.Execute != 1 || u.Review != 2 || u.PerApprover["rev"] != 0 {
		t.Fatalf("usage after refused claim = %+v", u)
	}
}

func TestChecksForAnotherHeadAreRefusedAndChangeNothing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		inReview bool
		success  bool
	}{
		{name: "red in review", inReview: true},
		{name: "green before review", success: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.backlog(1)
			h.approvedItem("I1", 1)
			c := h.claim("laptop")
			if tc.inReview {
				h.toReview(c.SliceID)
			} else {
				h.ok(h.eng.Submit(ctx(), "laptop", "", c.RunID, "metronome/"+c.SliceID))
				h.ok(h.eng.PROpened(ctx(), "github", "", c.SliceID, "acme/app", 10, "sha-"+c.SliceID))
			}
			before := h.slice(c.SliceID)
			pr := *before.PR
			before.PR = &pr
			usage := h.usage()
			res, err := h.eng.ChecksCompleted(ctx(), "github", "", c.SliceID, "old", tc.success)
			wantCode(t, err, flow.CodeConflict)
			if len(res.Events) != 0 {
				t.Fatalf("stale checks emitted events: %v", res.Events)
			}
			if sl := h.slice(c.SliceID); !reflect.DeepEqual(sl, before) {
				t.Fatalf("stale checks changed slice from %+v to %+v", before, sl)
			}
			if u := h.usage(); !reflect.DeepEqual(u, usage) {
				t.Fatalf("stale checks changed usage from %+v to %+v", usage, u)
			}
		})
	}
}

func TestReadyForReviewNeedsGreenChecksAndFreesExecute(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItem("I1", 1)
	c := h.claim("cloud")
	h.ok(h.eng.Submit(ctx(), "cloud", "", c.RunID, "metronome/"+c.SliceID))
	h.ok(h.eng.PROpened(ctx(), "github", "", c.SliceID, "acme/app", 7, "abc"))
	_, err := h.eng.ReadyForReview(ctx(), "github", "", c.SliceID)
	wantCode(t, err, flow.CodeConflict)
	h.ok(h.eng.ChecksCompleted(ctx(), "github", "", c.SliceID, "abc", true))
	h.ok(h.eng.ReadyForReview(ctx(), "github", "", c.SliceID))
	if u := h.usage(); u.Execute != 0 || u.Review != 1 || u.PerApprover["rev"] != 1 {
		t.Fatalf("usage in review = %+v", u)
	}
}

func TestHumanRejectionReturnsToExecuteKeepingTheReviewReservation(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItem("I1", 1)
	c := h.claim("cloud")
	h.toReview(c.SliceID)
	h.ok(h.eng.PRChangesRequested(ctx(), "github", "", c.SliceID, "rev"))
	sl := h.slice(c.SliceID)
	if sl.Status != flow.SliceQueued || !sl.Rework || sl.HoldsExecute || !sl.HoldsReview {
		t.Fatalf("rejected slice = %+v", sl)
	}
	r := h.ok(h.eng.Claim(ctx(), "cloud", "", "cloud", ""))
	if len(r.Events) == 0 {
		t.Fatal("rework claim emitted nothing")
	}
	if u := h.usage(); u.Execute != 1 || u.Review != 1 {
		t.Fatalf("usage after rework claim = %+v, want one of each", u)
	}
}

func TestApprovalOfTheLastPRTakesAReleaseSlot(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItem("I1", 2)
	a, b := h.claim("r1"), h.claim("r2")
	h.toReview(a.SliceID)
	h.toReview(b.SliceID)
	h.ok(h.eng.PRApproved(ctx(), "github", "", a.SliceID, "sha-"+a.SliceID, "rev"))
	if sl := h.slice(a.SliceID); sl.HoldsReview {
		t.Fatal("a sibling-waiting approved PR should free its Review slot")
	}
	if it := h.item("I1"); it.Status != flow.ItemExecuting {
		t.Fatalf("item = %s before the last approval", it.Status)
	}
	h.ok(h.eng.PRApproved(ctx(), "github", "", b.SliceID, "sha-"+b.SliceID, "rev"))
	it := h.item("I1")
	if it.Status != flow.ItemReleasing || !it.HoldsRelease {
		t.Fatalf("item = %+v, want releasing with a Release slot", it)
	}
	if u := h.usage(); u.Review != 0 || u.Release != 1 {
		t.Fatalf("usage = %+v", u)
	}
	_, err := h.eng.ReleaseDone(ctx(), "pat", "", "I1")
	wantCode(t, err, flow.CodeConflict)
	h.ok(h.eng.PRMerged(ctx(), "github", "", a.SliceID, ""))
	h.ok(h.eng.PRMerged(ctx(), "github", "", b.SliceID, ""))
	h.ok(h.eng.ReleaseDone(ctx(), "pat", "", "I1"))
	if it := h.item("I1"); it.Status != flow.ItemDone || it.HoldsRelease {
		t.Fatalf("item = %+v, want done", it)
	}
}

func TestFullReleaseBacksUpIntoReviewUntilASlotFrees(t *testing.T) {
	h := newHarness(t, func(c *flow.Config) { c.Limits.Release = 1 })
	h.backlog(2)
	h.approvedItem("I1", 1)
	h.approvedItem("I2", 1)
	first, second := h.claim("r1"), h.claim("r2")
	h.toReview(first.SliceID)
	h.toReview(second.SliceID)
	approve := func(id string) {
		h.ok(h.eng.PRApproved(ctx(), "github", "", id, "sha-"+id, "rev"))
	}
	approve(first.SliceID)
	approve(second.SliceID)
	waiting := h.item(h.slice(second.SliceID).ItemID)
	if waiting.Status != flow.ItemAwaitingRelease || !h.slice(second.SliceID).HoldsReview {
		t.Fatalf("second item = %s, slice holds review = %v; want awaiting release, holding Review",
			waiting.Status, h.slice(second.SliceID).HoldsReview)
	}
	h.ok(h.eng.PRMerged(ctx(), "github", "", first.SliceID, ""))
	h.ok(h.eng.ReleaseDone(ctx(), "pat", "", h.slice(first.SliceID).ItemID))
	if it := h.item(waiting.ID); it.Status != flow.ItemReleasing || h.slice(second.SliceID).HoldsReview {
		t.Fatalf("waiting item = %s after a Release slot freed, want releasing with Review freed", it.Status)
	}
}

func TestANewHeadOnAnApprovedSliceWhoseSiblingWaitsWithdrawsTheApproval(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItem("I1", 2)
	first, second := h.claim("r1").SliceID, h.claim("r2").SliceID
	h.toReview(first)
	h.toReview(second)
	h.ok(h.eng.PRApproved(ctx(), "github", "", first, "sha-"+first, "rev"))
	h.wantSlots(1, 0)
	h.newHead(first, "bbb")
	if sl := h.slice(first); sl.Status != flow.SlicePROpen || sl.PR.HeadSHA != "bbb" || sl.PR.ApprovedSHA != "" || sl.PR.ChecksRecorded || sl.PR.ChecksGreen || !sl.HoldsReview || sl.HoldsExecute {
		t.Fatalf("withdrawn slice = %+v, PR = %+v", sl, sl.PR)
	}
	h.wantSlots(2, 0)
	if it := h.item("I1"); it.Status != flow.ItemExecuting {
		t.Fatalf("item = %+v", it)
	}
	_, err := h.eng.ReadyForReview(ctx(), "github", "", first)
	wantCode(t, err, flow.CodeConflict)
	h.reapprove(first, "bbb")
	h.wantSlots(1, 0)
	h.ok(h.eng.PRApproved(ctx(), "github", "", second, "sha-"+second, "rev"))
	h.wantSlots(0, 1)
	if it := h.item("I1"); it.Status != flow.ItemReleasing || !it.HoldsRelease {
		t.Fatalf("item = %+v", it)
	}
}

func TestANewHeadOnAReleasingItemFreesItsReleaseSlot(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItem("I1", 1)
	id := h.claimAndApprove()
	h.wantSlots(0, 1)
	h.newHead(id, "bbb")
	h.wantSlots(1, 0)
	if it := h.item("I1"); it.Status != flow.ItemExecuting || it.HoldsRelease {
		t.Fatalf("item = %+v", it)
	}
	before, item, usage := h.slice(id), h.item("I1"), h.usage()
	h.eng = h.reload()
	if !reflect.DeepEqual(h.slice(id), before) || !reflect.DeepEqual(h.item("I1"), item) || !reflect.DeepEqual(h.usage(), usage) {
		t.Fatal("replay changed the withdrawn slice, item or usage")
	}
	_, err := h.eng.ReleaseDone(ctx(), "pat", "", "I1")
	wantCode(t, err, flow.CodeConflict)
	h.reapprove(id, "bbb")
	h.wantSlots(0, 1)
	if it := h.item("I1"); it.Status != flow.ItemReleasing || !it.HoldsRelease {
		t.Fatalf("item = %+v", it)
	}
}

func TestANewHeadOnAnItemAwaitingReleaseLeavesReleaseAndKeepsReview(t *testing.T) {
	h := newHarness(t, func(c *flow.Config) { c.Limits.Release = 1 })
	h.backlog(2)
	h.approvedItem("I1", 1)
	h.approvedItem("I2", 1)
	first, second := h.claimAndApprove(), h.claimAndApprove()
	h.wantSlots(1, 1)
	h.newHead(second, "bbb")
	if it := h.item("I2"); it.Status != flow.ItemExecuting || it.HoldsRelease || !h.slice(second).HoldsReview {
		t.Fatalf("item = %+v, slice = %+v", it, h.slice(second))
	}
	h.wantSlots(1, 1)
	h.ok(h.eng.PRMerged(ctx(), "github", "", first, ""))
	h.ok(h.eng.ReleaseDone(ctx(), "pat", "", "I1"))
	h.wantSlots(1, 0)
	if it := h.item("I2"); it.Status != flow.ItemExecuting {
		t.Fatalf("item = %+v", it)
	}
	h.reapprove(second, "bbb")
	h.wantSlots(0, 1)
}

func TestWithdrawingAnEarlierApprovalOfAnItemAwaitingReleaseMovesTheReviewSlot(t *testing.T) {
	h := newHarness(t, func(c *flow.Config) { c.Limits.Release = 1 })
	h.backlog(2)
	h.approvedItem("I1", 1)
	h.approvedItem("I2", 2)
	h.claimAndApprove()
	first, second := h.claimAndApprove(), h.claimAndApprove()
	if h.slice(first).HoldsReview || !h.slice(second).HoldsReview {
		t.Fatal("only the last approval should hold Review")
	}
	h.wantSlots(1, 1)
	h.newHead(first, "bbb")
	if sl := h.slice(second); sl.Status != flow.SliceApproved || sl.HoldsReview || !h.slice(first).HoldsReview {
		t.Fatalf("Review did not move: first = %+v, second = %+v", h.slice(first), sl)
	}
	if it := h.item("I2"); it.Status != flow.ItemExecuting {
		t.Fatalf("item = %+v", it)
	}
	h.wantSlots(1, 1)
	h.reapprove(first, "bbb")
	h.wantSlots(1, 1)
	if it := h.item("I2"); it.Status != flow.ItemAwaitingRelease || !h.slice(first).HoldsReview {
		t.Fatalf("item = %+v, slice = %+v", it, h.slice(first))
	}
}

func TestWithdrawalMovesReviewFromAMergedSiblingAwaitingRelease(t *testing.T) {
	for _, approveBeforeMerge := range []bool{false, true} {
		t.Run(fmt.Sprintf("approve before merge %v", approveBeforeMerge), func(t *testing.T) {
			h := newHarness(t, func(c *flow.Config) { c.Limits.Release = 1 })
			h.backlog(2)
			h.approvedItem("I1", 1)
			h.approvedItem("I2", 2)
			h.claimAndApprove()
			first := h.claimAndApprove()
			second := h.claim("laptop").SliceID
			h.toReview(second)
			if approveBeforeMerge {
				h.ok(h.eng.PRApproved(ctx(), "github", "", second, "sha-"+second, "rev"))
			}
			h.ok(h.eng.PRMerged(ctx(), "github", "", second, "pat"))
			if it := h.item("I2"); it.Status != flow.ItemAwaitingRelease {
				t.Fatalf("item = %+v", it)
			}
			if sl := h.slice(second); sl.Status != flow.SliceMerged || !sl.HoldsReview || h.slice(first).HoldsReview {
				t.Fatalf("merged sibling should hold Review: first = %+v, second = %+v", h.slice(first), sl)
			}
			h.wantSlots(1, 1)
			h.newHead(first, "bbb")
			h.wantSlots(1, 1)
			if sl := h.slice(second); sl.Status != flow.SliceMerged || sl.HoldsReview || !h.slice(first).HoldsReview {
				t.Fatalf("Review did not move: first = %+v, second = %+v", h.slice(first), sl)
			}
			if it := h.item("I2"); it.Status != flow.ItemExecuting || it.HoldsRelease {
				t.Fatalf("item = %+v", it)
			}
			h.reapprove(first, "bbb")
			h.wantSlots(1, 1)
			if sl := h.slice(second); sl.Status != flow.SliceMerged || sl.HoldsReview || !h.slice(first).HoldsReview {
				t.Fatalf("only the re-approved slice should hold Review: first = %+v, second = %+v", h.slice(first), sl)
			}
			if it := h.item("I2"); it.Status != flow.ItemAwaitingRelease || it.HoldsRelease {
				t.Fatalf("item = %+v", it)
			}
		})
	}
}

func TestAReleaseSlotFreedByAWithdrawalGoesToTheNextWaitingItem(t *testing.T) {
	h := newHarness(t, func(c *flow.Config) { c.Limits.Release = 1 })
	h.backlog(2)
	h.approvedItem("I1", 1)
	h.approvedItem("I2", 1)
	first, second := h.claimAndApprove(), h.claimAndApprove()
	h.newHead(first, "bbb")
	h.wantSlots(1, 1)
	if it := h.item("I2"); it.Status != flow.ItemReleasing || !it.HoldsRelease || h.slice(second).HoldsReview {
		t.Fatalf("item = %+v, slice = %+v", it, h.slice(second))
	}
	if it := h.item("I1"); it.Status != flow.ItemExecuting || it.HoldsRelease {
		t.Fatalf("item = %+v", it)
	}
	h.reapprove(first, "bbb")
	h.wantSlots(1, 1)
	if it := h.item("I1"); it.Status != flow.ItemAwaitingRelease || !h.slice(first).HoldsReview {
		t.Fatalf("item = %+v, slice = %+v", it, h.slice(first))
	}
}

func TestAWithdrawalRestoresReviewEvenOverTheLimit(t *testing.T) {
	h := newHarness(t, func(c *flow.Config) { c.Limits.Review = 2 })
	h.backlog(2)
	h.approvedItem("I1", 2)
	h.approvedItem("I2", 2)
	first := h.claim("r1").SliceID
	h.claim("r2")
	h.toReview(first)
	h.ok(h.eng.PRApproved(ctx(), "github", "", first, "sha-"+first, "rev"))
	h.claim("r3")
	h.wantSlots(2, 0)
	h.newHead(first, "bbb")
	h.wantSlots(3, 0)
	if !h.slice(first).HoldsReview {
		t.Fatal("withdrawn slice must hold Review even over the limit")
	}
	_, err := h.eng.Claim(ctx(), "r4", "", "r4", "")
	wantCode(t, err, flow.CodeNoCapacity)
	h.reapprove(first, "bbb")
	h.wantSlots(2, 0)
}

func TestTheSameHeadOnAnApprovedSliceIsRefusedAndChangesNothing(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItem("I1", 1)
	id := h.claimAndApprove()
	before, usage := h.slice(id), h.usage()
	pr := *before.PR
	before.PR = &pr
	for _, number := range []int{10, 11} {
		res, err := h.eng.PROpened(ctx(), "github", "", id, "acme/app", number, pr.HeadSHA)
		wantCode(t, err, flow.CodeConflict)
		if len(res.Events) != 0 || !reflect.DeepEqual(h.slice(id), before) || !reflect.DeepEqual(h.usage(), usage) {
			t.Fatalf("same head changed approval or slots: %+v", res)
		}
	}
}

func TestAMergeAtAMovedHeadIsApprovedByWhoeverMerged(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItem("I1", 1)
	id := h.claimAndApprove()
	h.newHead(id, "bbb")
	res := h.ok(h.eng.PRMerged(ctx(), "github", "", id, "pat"))
	if sl := h.slice(id); sl.Status != flow.SliceMerged || sl.PR.ApprovedSHA != "bbb" || sl.HoldsReview || sl.HoldsExecute {
		t.Fatalf("merged slice = %+v, PR = %+v", sl, sl.PR)
	}
	if len(res.Events) != 4 || res.Events[1].Type != flow.EvPRApproved {
		t.Fatalf("merge events = %+v", res.Events)
	}
	h.wantSlots(0, 1)
}

func TestRepeatedRequestIDIsIdempotent(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItem("I1", 2)
	r1 := h.ok(h.eng.Claim(ctx(), "laptop", "req-1", "laptop", ""))
	r2 := h.ok(h.eng.Claim(ctx(), "laptop", "req-1", "laptop", ""))
	c1, _ := flow.ClaimFrom(r1)
	c2, ok := flow.ClaimFrom(r2)
	if !r2.Duplicate || !ok || c1 != c2 {
		t.Fatalf("replayed claim = %+v (dup %v), want the original %+v", c2, r2.Duplicate, c1)
	}
	if u := h.usage(); u.Execute != 1 {
		t.Fatalf("execute usage = %d after a replayed claim, want 1", u.Execute)
	}
}

func TestStateIsRebuiltFromTheEventLog(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(3)
	h.approvedItem("I1", 2)
	c := h.claim("laptop")
	h.ok(h.eng.FlagRun(ctx(), "laptop", "", c.RunID, flow.AskAnswer, "?", ""))
	before := h.usage()
	h.eng = h.reload()
	if after := h.usage(); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("usage after replay = %+v, want %+v", after, before)
	}
	if sl := h.slice(c.SliceID); sl.Status != flow.SliceFlagged {
		t.Fatalf("slice after replay = %s", sl.Status)
	}
	if next := h.claim("cloud"); next.RunID == c.RunID {
		t.Fatal("run ids must keep counting after a replay")
	}
}

func TestClaimHistoryIsRebuiltFromTheEventLog(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItem("I1", 1)
	first := h.claim("laptop")
	h.ok(h.eng.FlagRun(ctx(), "laptop", "", first.RunID, flow.AskAnswer, "CSV or JSON?", ""))
	h.clock.advance(time.Minute)
	h.ok(h.eng.ResolveFlag(ctx(), "pat", "", "F2", "CSV"))
	second := h.claim("cloud")
	h.ok(h.eng.Submit(ctx(), "cloud", "", second.RunID, "metronome/I1.1"))
	h.ok(h.eng.PROpened(ctx(), "github", "", second.SliceID, "acme/app", 7, "abc123"))
	h.ok(h.eng.ChecksCompleted(ctx(), "github", "", second.SliceID, "abc123", false))
	third := h.claim("cloud")
	want := flow.SliceHistory{Reason: flow.ResumeChecksFailed, Rework: true, ReworkReason: flow.ResumeChecksFailed,
		FailedHead: "abc123", PreviousRun: second.RunID, Branch: "metronome/I1.1",
		PR:   &flow.HistoryPR{Repo: "acme/app", Number: 7, HeadSHA: "abc123"},
		Asks: []flow.HistoryAsk{{ID: "F2", Type: flow.AskAnswer, Question: "CSV or JSON?", Answer: "CSV", AnsweredBy: "pat", AnsweredAt: h.clock.Now()}}}
	if history := h.history(third.RunID); !reflect.DeepEqual(history, want) {
		t.Fatalf("history = %+v, want %+v", history, want)
	}
	h.ok(h.eng.PROpened(ctx(), "github", "", third.SliceID, "acme/app", 7, "def456"))
	h.ok(h.eng.ChecksCompleted(ctx(), "github", "", third.SliceID, "def456", true))
	if history := h.history(third.RunID); !reflect.DeepEqual(history, want) {
		t.Fatalf("history changed after PR update: %+v", history)
	}
	before := h.slice(third.SliceID)
	histories := []flow.SliceHistory{h.history(first.RunID), h.history(second.RunID), h.history(third.RunID)}
	h.eng = h.reload()
	if after := h.slice(third.SliceID); !reflect.DeepEqual(after, before) {
		t.Fatalf("slice after replay = %+v, want %+v", after, before)
	}
	if after := []flow.SliceHistory{h.history(first.RunID), h.history(second.RunID), h.history(third.RunID)}; !reflect.DeepEqual(after, histories) {
		t.Fatalf("histories after replay = %+v, want %+v", after, histories)
	}
}

func TestAMergeBeforeApprovalIsApprovedByWhoeverMerged(t *testing.T) {
	for _, inReview := range []bool{false, true} {
		t.Run(fmt.Sprintf("in review %v", inReview), func(t *testing.T) {
			h := newHarness(t, nil)
			h.backlog(1)
			h.approvedItem("I1", 1)
			c := h.claim("laptop")
			if inReview {
				h.toReview(c.SliceID)
			} else {
				h.ok(h.eng.Submit(ctx(), "runner", "", c.RunID, "metronome/"+c.SliceID))
				h.ok(h.eng.PROpened(ctx(), "github", "", c.SliceID, "acme/app", 10, "sha-x"))
			}
			h.ok(h.eng.PRMerged(ctx(), "github", "", c.SliceID, "pat"))
			sl := h.slice(c.SliceID)
			if sl.Status != flow.SliceMerged || sl.PR.ApprovedSHA != sl.PR.HeadSHA || sl.HoldsExecute || sl.HoldsReview {
				t.Fatalf("slice = %+v, want merged and approved at its head with no slots", sl)
			}
			if u := h.usage(); u.Execute != 0 || u.Review != 0 || u.Release != 1 {
				t.Fatalf("usage = %+v, want only the Release slot held", u)
			}
		})
	}
}

func TestAMergeBeforeApprovalNeedsTheMerger(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItem("I1", 1)
	c := h.claim("laptop")
	h.toReview(c.SliceID)
	_, err := h.eng.PRMerged(ctx(), "github", "", c.SliceID, "")
	wantCode(t, err, flow.CodeInvalid)
}

func TestAMergeIsRefusedWhileTheSliceIsBeingReworked(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItem("I1", 1)
	c := h.claim("laptop")
	h.toReview(c.SliceID)
	h.ok(h.eng.PRChangesRequested(ctx(), "github", "", c.SliceID, "rev"))
	_, err := h.eng.PRMerged(ctx(), "github", "", c.SliceID, "pat")
	wantCode(t, err, flow.CodeConflict)
}

func TestPlanApprovalStoresTheConfirmedTier(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItemAt("I1", 1, 0)
	if it := h.item("I1"); it.ConfirmedTier != 0 {
		t.Fatalf("confirmed tier = %d, want 0", it.ConfirmedTier)
	}
	h.eng = h.reload()
	if it := h.item("I1"); it.ConfirmedTier != 0 {
		t.Fatalf("replayed confirmed tier = %d, want 0", it.ConfirmedTier)
	}
}

func (h *harness) wantNoRelease(itemID string, tier int) {
	h.t.Helper()
	_, err := h.eng.ReleaseDone(ctx(), "pat", "", itemID)
	wantCode(h.t, err, flow.CodeConflict)
	if !strings.Contains(err.Error(), fmt.Sprintf("tier %d", tier)) || !strings.Contains(err.Error(), "last slice merges") {
		h.t.Fatalf("release-done error = %v", err)
	}
}

func (h *harness) wantDone(itemID string) {
	h.t.Helper()
	it := h.item(itemID)
	if it.Status != flow.ItemDone || it.HoldsRelease {
		h.t.Fatalf("item = %+v, want done", it)
	}
	for _, id := range it.Slices {
		if sl := h.slice(id); sl.Status != flow.SliceMerged || sl.HoldsExecute || sl.HoldsReview {
			h.t.Fatalf("slice = %+v, want merged with no slots", sl)
		}
	}
}

func TestItemsFinishAtTheGateForTheirConfirmedTier(t *testing.T) {
	for _, tier := range []int{0, 1, 2, 3} {
		for _, count := range []int{1, 2} {
			for _, reverse := range []bool{false, true} {
				if count == 1 && reverse {
					continue
				}
				t.Run(fmt.Sprintf("tier %d/slices %d/reverse %v", tier, count, reverse), func(t *testing.T) {
					h := newHarness(t, nil)
					h.backlog(1)
					h.approvedItemAt("I1", count, tier)
					ids := make([]string, 0, count)
					for index := 0; index < count; index++ {
						id := h.claimAndApprove()
						ids = append(ids, id)
						if index < count-1 {
							if h.slice(id).HoldsReview {
								t.Fatal("earlier approval kept Review")
							}
							h.wantSlots(0, 0)
						}
					}
					status, review, release := flow.ItemExecuting, 1, 0
					if tier >= 2 {
						status, review, release = flow.ItemReleasing, 0, 1
					} else {
						h.wantNoRelease("I1", tier)
					}
					if it := h.item("I1"); it.Status != status || it.HoldsRelease != (tier >= 2) || it.ConfirmedTier != tier {
						t.Fatalf("approved item = %+v", it)
					}
					h.wantSlots(review, release)
					if reverse {
						ids[0], ids[1] = ids[1], ids[0]
					}
					for index, id := range ids {
						result := h.ok(h.eng.PRMerged(ctx(), "github", "", id, ""))
						if index == count-1 && tier < 2 {
							if len(result.Events) != 2 || result.Events[1].Type != flow.EvItemDone {
								t.Fatalf("last merge events = %+v", result.Events)
							}
						} else {
							if len(result.Events) != 1 || h.item("I1").Status != status {
								t.Fatalf("merge changed item prematurely: %+v", result.Events)
							}
							if tier < 2 && reverse {
								review = 0
							}
							h.wantSlots(review, release)
						}
						if repeated := h.ok(h.eng.PRMerged(ctx(), "github", "", id, "")); len(repeated.Events) != 0 {
							t.Fatalf("repeated merge = %+v", repeated.Events)
						}
					}
					if tier >= 2 {
						h.ok(h.eng.ReleaseDone(ctx(), "pat", "", "I1"))
					} else {
						h.wantNoRelease("I1", tier)
					}
					h.wantDone("I1")
					h.wantSlots(0, 0)
					if h.usage().Execute != 0 {
						t.Fatalf("usage = %+v", h.usage())
					}
					before, usage := h.item("I1"), h.usage()
					var beforeSlices []flow.Slice
					for _, id := range ids {
						beforeSlices = append(beforeSlices, h.slice(id))
					}
					h.eng = h.reload()
					if !reflect.DeepEqual(before, h.item("I1")) || !reflect.DeepEqual(usage, h.usage()) {
						t.Fatal("replay changed item or usage")
					}
					for index, id := range ids {
						if !reflect.DeepEqual(beforeSlices[index], h.slice(id)) {
							t.Fatalf("replay changed slice %s", id)
						}
					}
				})
			}
		}
	}
}

func TestLowTierNonFinalMergeFreesReview(t *testing.T) {
	for _, tier := range []int{0, 1} {
		for _, status := range []flow.SliceStatus{flow.SliceApproved, flow.SliceInReview, flow.SlicePROpen} {
			t.Run(fmt.Sprintf("tier %d/%s", tier, status), func(t *testing.T) {
				h := newHarness(t, func(c *flow.Config) { c.Limits.Review = 1 })
				h.backlog(2)
				h.approvedItemAt("I1", 2, tier)
				h.approvedItemAt("I2", 1, tier)
				first := h.claimAndApprove()
				claim := h.claim("laptop")
				second := claim.SliceID
				if status == flow.SlicePROpen {
					h.ok(h.eng.Submit(ctx(), "runner", "", claim.RunID, "metronome/"+second))
					h.ok(h.eng.PROpened(ctx(), "github", "", second, "acme/app", 11, "sha-"+second))
				} else {
					h.toReview(second)
					if status == flow.SliceApproved {
						h.ok(h.eng.PRApproved(ctx(), "github", "", second, "sha-"+second, "rev"))
					}
				}
				h.wantSlots(1, 0)
				_, err := h.eng.Claim(ctx(), "runner", "", "probe", "I2.1")
				wantCode(t, err, flow.CodeNoCapacity)
				h.ok(h.eng.PRMerged(ctx(), "github", "", second, "rev"))
				for _, replay := range []bool{false, true} {
					if replay {
						h.eng = h.reload()
					}
					if sl := h.slice(second); sl.Status != flow.SliceMerged || sl.HoldsReview || sl.HoldsExecute {
						t.Fatalf("merged slice holds slots (replay %v): %+v", replay, sl)
					}
					if it := h.item("I1"); it.Status != flow.ItemExecuting || it.HoldsRelease || h.slice(first).Status != flow.SliceApproved {
						t.Fatalf("item finished early (replay %v): %+v", replay, it)
					}
					h.wantSlots(0, 0)
				}
				h.ok(h.eng.Claim(ctx(), "runner", "", "probe", "I2.1"))
				h.ok(h.eng.PRMerged(ctx(), "github", "", first, "rev"))
				h.wantDone("I1")
				h.wantSlots(1, 0)
			})
		}
	}
}

func TestHistoricalMergeWithoutReviewReleaseReplaysUnchanged(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItemAt("I1", 2, 1)
	first, second := h.claimAndApprove(), h.claimAndApprove()
	if _, err := h.store.Append(ctx(), []flow.Event{{ID: "legacy-merge", Type: flow.EvPRMerged, At: h.clock.Now(), Actor: "github", Data: json.RawMessage(fmt.Sprintf(`{"slice_id":%q}`, second))}}); err != nil {
		t.Fatal(err)
	}
	h.eng = h.reload()
	if sl := h.slice(second); sl.Status != flow.SliceMerged || !sl.HoldsReview {
		t.Fatalf("historical merge changed meaning: %+v", sl)
	}
	h.wantSlots(1, 0)
	h.ok(h.eng.PRMerged(ctx(), "github", "", first, "rev"))
	h.wantDone("I1")
	h.wantSlots(0, 0)
}

func TestMergeBeforeApprovalFinishesAtTheGateForItsTier(t *testing.T) {
	for _, tier := range []int{0, 1, 2} {
		for _, count := range []int{1, 2} {
			for _, inReview := range []bool{false, true} {
				t.Run(fmt.Sprintf("tier %d/slices %d/in review %v", tier, count, inReview), func(t *testing.T) {
					h := newHarness(t, nil)
					h.backlog(1)
					h.approvedItemAt("I1", count, tier)
					if count == 2 {
						first := h.claimAndApprove()
						h.ok(h.eng.PRMerged(ctx(), "github", "", first, ""))
						h.wantSlots(0, 0)
					}
					claim := h.claim("laptop")
					id := claim.SliceID
					var wantEvents []string
					if inReview {
						h.toReview(id)
					} else {
						h.ok(h.eng.Submit(ctx(), "runner", "", claim.RunID, "metronome/"+id))
						h.ok(h.eng.PROpened(ctx(), "github", "", id, "acme/app", 10, "sha-x"))
						wantEvents = append(wantEvents, flow.EvPRReadyForReview)
					}
					_, err := h.eng.PRMerged(ctx(), "github", "", id, "")
					wantCode(t, err, flow.CodeInvalid)
					wantEvents = append(wantEvents, flow.EvPRApproved)
					if tier >= 2 {
						wantEvents = append(wantEvents, flow.EvReleaseSlotTaken)
					}
					wantEvents = append(wantEvents, flow.EvPRMerged)
					if tier < 2 {
						wantEvents = append(wantEvents, flow.EvItemDone)
					}
					result := h.ok(h.eng.PRMerged(ctx(), "github", "", id, "pat"))
					var gotEvents []string
					for _, event := range result.Events {
						gotEvents = append(gotEvents, event.Type)
						if event.Type == flow.EvPRApproved {
							var approval struct {
								By string `json:"by"`
							}
							if err := json.Unmarshal(event.Data, &approval); err != nil || approval.By != "pat" {
								t.Fatalf("approval = %s (%v)", event.Data, err)
							}
						}
					}
					if !reflect.DeepEqual(gotEvents, wantEvents) {
						t.Fatalf("events = %v, want %v", gotEvents, wantEvents)
					}
					if sl := h.slice(id); sl.PR.ApprovedSHA != sl.PR.HeadSHA {
						t.Fatalf("PR = %+v", sl.PR)
					}
					if tier >= 2 {
						h.wantSlots(0, 1)
						if h.item("I1").Status != flow.ItemReleasing {
							t.Fatal("item must wait for release")
						}
						h.ok(h.eng.ReleaseDone(ctx(), "pat", "", "I1"))
					}
					h.wantDone("I1")
					h.wantSlots(0, 0)
					if h.usage().Execute != 0 {
						t.Fatalf("usage = %+v", h.usage())
					}
				})
			}
		}
	}
}

func TestALowTierItemNeverTakesAReleaseSlotWhenReleaseIsFull(t *testing.T) {
	h := newHarness(t, func(c *flow.Config) { c.Limits.Release = 1 })
	h.backlog(2)
	h.approvedItem("I1", 1)
	h.approvedItemAt("I2", 1, 1)
	first := h.claimAndApprove()
	second := h.claimAndApprove()
	if it := h.item("I2"); it.Status != flow.ItemExecuting || it.HoldsRelease {
		t.Fatalf("low-tier item = %+v", it)
	}
	h.wantSlots(1, 1)
	h.ok(h.eng.PRMerged(ctx(), "github", "", second, ""))
	h.wantDone("I2")
	h.wantSlots(0, 1)
	h.ok(h.eng.PRMerged(ctx(), "github", "", first, ""))
	h.ok(h.eng.ReleaseDone(ctx(), "pat", "", "I1"))
	h.wantDone("I1")
	h.wantSlots(0, 0)
}

func TestWithdrawingAnApprovalOfALowTierItem(t *testing.T) {
	for _, tier := range []int{0, 1} {
		for _, siblingMerged := range []bool{false, true} {
			for _, mergeBeforeApproval := range []bool{false, true} {
				t.Run(fmt.Sprintf("tier %d/merged sibling %v/merge before approval %v", tier, siblingMerged, mergeBeforeApproval), func(t *testing.T) {
					h := newHarness(t, nil)
					h.backlog(1)
					h.approvedItemAt("I1", 2, tier)
					first, second := h.claimAndApprove(), h.claimAndApprove()
					h.wantSlots(1, 0)
					if siblingMerged {
						h.ok(h.eng.PRMerged(ctx(), "github", "", second, ""))
						h.wantSlots(0, 0)
					}
					h.newHead(first, "bbb")
					if sl := h.slice(first); sl.Status != flow.SlicePROpen || !sl.HoldsReview || h.slice(second).HoldsReview {
						t.Fatalf("withdrawal did not move Review: first = %+v, second = %+v", sl, h.slice(second))
					}
					if it := h.item("I1"); it.Status != flow.ItemExecuting || it.HoldsRelease {
						t.Fatalf("item = %+v", it)
					}
					h.wantSlots(1, 0)
					if !mergeBeforeApproval {
						h.reapprove(first, "bbb")
						h.wantSlots(1, 0)
					}
					h.ok(h.eng.PRMerged(ctx(), "github", "", first, "pat"))
					if !siblingMerged {
						if h.item("I1").Status != flow.ItemExecuting {
							t.Fatal("item finished before its last merge")
						}
						h.ok(h.eng.PRMerged(ctx(), "github", "", second, ""))
					}
					if sl := h.slice(first); sl.PR.ApprovedSHA != "bbb" {
						t.Fatalf("PR = %+v", sl.PR)
					}
					h.wantDone("I1")
					h.wantSlots(0, 0)
				})
			}
		}
	}
}

func (h *harness) appendLegacyLowTierItem(number int, releaseEvent string) {
	h.t.Helper()
	itemID := fmt.Sprintf("I%d", number)
	sliceID := itemID + ".1"
	runID := fmt.Sprintf("R%d", number)
	item := fmt.Sprintf(`{"item_id":%q}`, itemID)
	slice := fmt.Sprintf(`{"slice_id":%q}`, sliceID)
	payloads := []struct {
		typ, data string
	}{
		{flow.EvItemAdded, fmt.Sprintf(`{"item_id":%q,"ref":"ACME-%d","title":"example","owner":"pat","predicted_tier":1,"rank":%d}`, itemID, number, number)},
		{flow.EvPlanStarted, item},
		{flow.EvPlanProposed, fmt.Sprintf(`{"item_id":%q,"slices":[{"title":"example","repo":"acme/app","approvers":["rev"]}],"approver":"pat"}`, itemID)},
		{flow.EvPlanApproved, fmt.Sprintf(`{"item_id":%q,"confirmed_tier":1,"slice_ids":[%q]}`, itemID, sliceID)},
		{flow.EvSliceClaimed, fmt.Sprintf(`{"run_id":%q,"slice_id":%q,"runner_id":"laptop","lease_until":%q,"takes_execute":true,"takes_review":true}`, runID, sliceID, h.clock.Now().Add(time.Minute).Format(time.RFC3339))},
		{flow.EvRunClosed, fmt.Sprintf(`{"run_id":%q,"reason":"submitted","branch":"metronome/%s"}`, runID, sliceID)},
		{flow.EvPROpened, fmt.Sprintf(`{"slice_id":%q,"repo":"acme/app","number":%d,"head_sha":"abc"}`, sliceID, number)},
		{flow.EvChecksCompleted, fmt.Sprintf(`{"slice_id":%q,"head_sha":"abc","success":true}`, sliceID)},
		{flow.EvPRReadyForReview, slice},
		{flow.EvPRApproved, fmt.Sprintf(`{"slice_id":%q,"head_sha":"abc","by":"rev","keeps_review":true}`, sliceID)},
		{releaseEvent, item},
	}
	var events []flow.Event
	for index, payload := range payloads {
		events = append(events, flow.Event{ID: fmt.Sprintf("legacy-%s-%d", itemID, index), Type: payload.typ, At: h.clock.Now(), Actor: "pat", Data: json.RawMessage(payload.data)})
	}
	if _, err := h.store.Append(ctx(), events); err != nil {
		h.t.Fatal(err)
	}
}

func TestALegacyLowTierReleasingItemStillFinishesWithReleaseDone(t *testing.T) {
	h := newHarness(t, nil)
	h.appendLegacyLowTierItem(1, flow.EvReleaseSlotTaken)
	h.eng = h.reload()
	if it := h.item("I1"); it.ConfirmedTier != 1 || it.Status != flow.ItemReleasing || !it.HoldsRelease {
		t.Fatalf("replayed item = %+v", it)
	}
	h.wantSlots(0, 1)
	result := h.ok(h.eng.PRMerged(ctx(), "github", "", "I1.1", ""))
	if len(result.Events) != 1 || result.Events[0].Type != flow.EvPRMerged || h.item("I1").Status != flow.ItemReleasing {
		t.Fatalf("legacy merge = %+v", result.Events)
	}
	h.wantSlots(0, 1)
	h.ok(h.eng.ReleaseDone(ctx(), "pat", "", "I1"))
	h.wantDone("I1")
	h.wantSlots(0, 0)
}

func TestALegacyLowTierItemAwaitingReleaseStillFinishesWithReleaseDone(t *testing.T) {
	h := newHarness(t, func(c *flow.Config) { c.Limits.Release = 1 })
	h.appendLegacyLowTierItem(1, flow.EvReleaseSlotTaken)
	h.appendLegacyLowTierItem(2, flow.EvItemAwaitsRelease)
	h.eng = h.reload()
	if it := h.item("I2"); it.Status != flow.ItemAwaitingRelease || it.HoldsRelease || it.ConfirmedTier != 1 {
		t.Fatalf("replayed awaiting item = %+v", it)
	}
	h.wantSlots(1, 1)
	_, err := h.eng.ReleaseDone(ctx(), "pat", "", "I2")
	wantCode(t, err, flow.CodeConflict)
	if !strings.Contains(err.Error(), "awaiting_release, not releasing") {
		t.Fatalf("legacy release error = %v", err)
	}
	h.ok(h.eng.PRMerged(ctx(), "github", "", "I2.1", ""))
	if h.item("I2").Status != flow.ItemAwaitingRelease || !h.slice("I2.1").HoldsReview {
		t.Fatal("legacy merge changed the waiting release state")
	}
	h.wantSlots(1, 1)
	h.ok(h.eng.PRMerged(ctx(), "github", "", "I1.1", ""))
	h.ok(h.eng.ReleaseDone(ctx(), "pat", "", "I1"))
	h.wantDone("I1")
	if it := h.item("I2"); it.Status != flow.ItemReleasing || !it.HoldsRelease {
		t.Fatalf("awaiting item did not receive Release: %+v", it)
	}
	h.wantSlots(0, 1)
	h.ok(h.eng.ReleaseDone(ctx(), "pat", "", "I2"))
	h.wantDone("I2")
	h.wantSlots(0, 0)
}

func TestALegacyLowTierItemUsesTheNewRuleAfterWithdrawal(t *testing.T) {
	h := newHarness(t, nil)
	h.appendLegacyLowTierItem(1, flow.EvReleaseSlotTaken)
	h.eng = h.reload()
	h.newHead("I1.1", "bbb")
	h.wantSlots(1, 0)
	h.reapprove("I1.1", "bbb")
	h.wantSlots(1, 0)
	h.ok(h.eng.PRMerged(ctx(), "github", "", "I1.1", ""))
	h.wantDone("I1")
	h.wantSlots(0, 0)
}
