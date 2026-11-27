package flow_test

import (
	"context"
	"fmt"
	"reflect"
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
		items = append(items, flow.BacklogItem{Ref: fmt.Sprintf("ACME-%d", i), Title: fmt.Sprintf("item %d", i), Owner: "pat", PredictedTier: 1})
	}
	h.ok(h.eng.SyncBacklog(ctx(), "test", "", items))
}

func (h *harness) approvedItem(itemID string, slices int) {
	h.t.Helper()
	h.ok(h.eng.StartPlan(ctx(), "test", "", itemID))
	var specs []flow.SliceSpec
	for i := 1; i <= slices; i++ {
		specs = append(specs, flow.SliceSpec{Title: fmt.Sprintf("slice %d", i), Repo: "acme/app", Approvers: []string{"rev"}})
	}
	h.ok(h.eng.ProposePlan(ctx(), "test", "", itemID, specs, ""))
	h.ok(h.eng.ApprovePlan(ctx(), "pat", "", itemID, nil))
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
