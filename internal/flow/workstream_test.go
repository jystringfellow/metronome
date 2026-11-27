package flow_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/flow"
)

func (h *harness) workstream(name string, limits flow.PartialLimits) {
	h.t.Helper()
	h.ok(h.eng.CreateWorkstream(ctx(), "pat", "", name, limits))
}

func (h *harness) backlogIn(ws string, refs ...string) {
	h.t.Helper()
	var items []flow.BacklogItem
	for _, ref := range refs {
		items = append(items, flow.BacklogItem{Ref: ref, Title: ref, Owner: "pat", PredictedTier: intp(2)})
	}
	h.ok(h.eng.SyncBacklog(ctx(), "pat", "", ws, items))
}

func (h *harness) claimIn(ws string) flow.Claim {
	h.t.Helper()
	result := h.ok(h.eng.Claim(ctx(), "pat", "", "laptop", ws, ""))
	claim, ok := flow.ClaimFrom(result)
	if !ok || claim.Workstream != ws {
		h.t.Fatalf("claim = %+v", claim)
	}
	return claim
}

func TestWorkstreamLimitsAndReplay(t *testing.T) {
	h := newHarness(t, nil)
	h.workstream("acme", flow.PartialLimits{Execute: intp(1), Review: intp(1)})
	h.workstream("example", flow.PartialLimits{})
	h.backlogIn("acme", "A", "B")
	h.backlogIn("example", "C")
	h.approvedItem("I1", 1)
	h.approvedItem("I2", 1)
	h.approvedItem("I3", 1)
	first := h.claimIn("acme")
	_, err := h.eng.Claim(ctx(), "pat", "", "laptop", "acme", "")
	wantCode(t, err, flow.CodeNoCapacity)
	h.toReview(first.SliceID)
	_, err = h.eng.Claim(ctx(), "pat", "", "laptop", "acme", "")
	wantCode(t, err, flow.CodeNoCapacity)
	second := h.claimIn("example")
	h.toReview(second.SliceID)
	if h.usage().PerApprover["rev"] != 2 {
		t.Fatal("approver usage is not global")
	}
	h.ok(h.eng.SetWorkstreamLimits(ctx(), "pat", "", "acme", flow.PartialLimits{Review: intp(0)}))
	var before []flow.WorkstreamView
	h.eng.View(func(s *flow.State) {
		before = s.WorkstreamViews()
		if s.StageUsage("acme").Review != 1 || s.StageUsage("example").Review != 1 || s.Usage().Review != 2 {
			t.Fatal("incorrect review usage")
		}
		if s.Workstreams["example"].Limits != h.cfg.Limits.Stages() {
			t.Fatal("wrong default limits")
		}
	})
	h.eng = h.reload()
	h.eng.View(func(s *flow.State) {
		if !reflect.DeepEqual(before, s.WorkstreamViews()) {
			t.Fatal("workstream view changed on replay")
		}
	})
}

func TestWorkstreamZeroCapacity(t *testing.T) {
	h := newHarness(t, nil)
	h.workstream("source", flow.PartialLimits{Plan: intp(0), Execute: intp(0), Review: intp(0), Release: intp(0)})
	h.workstream("empty", flow.PartialLimits{Ready: intp(0)})
	h.backlogIn("source", "A")
	h.backlogIn("empty", "B")
	h.backlogIn("", "C")
	_, err := h.eng.StartPlan(ctx(), "pat", "", "source", "")
	wantCode(t, err, flow.CodeNoCapacity)
	_, err = h.eng.StartPlan(ctx(), "pat", "", "", "I1")
	wantCode(t, err, flow.CodeNoCapacity)
	h.ok(h.eng.StartPlan(ctx(), "pat", "", "", ""))
	if h.item("I2").Status != flow.ItemPlanning || h.item("I1").Status != flow.ItemReady {
		t.Fatal("plan did not skip source")
	}
	h.ok(h.eng.ProposePlan(ctx(), "pat", "", "I2", []flow.SliceSpec{{Title: "example", Repo: "acme/app"}}, ""))
	h.ok(h.eng.ApprovePlan(ctx(), "pat", "", "I2", nil))
	h.ok(h.eng.SetWorkstreamLimits(ctx(), "pat", "", "source", flow.PartialLimits{Plan: intp(1)}))
	h.approvedItem("I1", 1)
	_, err = h.eng.Claim(ctx(), "pat", "", "laptop", "source", "")
	wantCode(t, err, flow.CodeNoCapacity)
	if claim := h.claim("laptop"); claim.Workstream != "default" {
		t.Fatalf("claim = %+v", claim)
	}
}

func TestWorkstreamClaimOrder(t *testing.T) {
	for _, addedLater := range []bool{false, true} {
		h := newHarness(t, nil)
		h.workstream("acme", flow.PartialLimits{})
		h.backlogIn("", "A", "B")
		if addedLater {
			h.clock.advance(time.Second)
		}
		h.backlogIn("acme", "A", "B", "C")
		for _, id := range []string{"I1", "I2", "I3"} {
			h.approvedItem(id, 1)
		}
		if h.item("I3").Rank != 3 {
			t.Fatal("rank should reflect source position")
		}
		_, err := h.eng.Claim(ctx(), "pat", "", "laptop", "acme", "I1.1")
		wantCode(t, err, flow.CodeConflict)
		want := []string{"I3.1", "I1.1", "I2.1"}
		if addedLater {
			want = []string{"I1.1", "I2.1", "I3.1"}
		}
		for _, id := range want {
			if claim := h.claim("laptop"); claim.SliceID != id {
				t.Fatalf("got %s, want %s", claim.SliceID, id)
			}
		}
	}
}

func TestWorkstreamReleaseSlots(t *testing.T) {
	h := newHarness(t, func(c *flow.Config) { c.Limits.Release = 1 })
	h.workstream("acme", flow.PartialLimits{Release: intp(1)})
	h.workstream("source", flow.PartialLimits{Release: intp(0)})
	h.backlogIn("acme", "A", "B")
	h.backlogIn("", "C", "D")
	h.backlogIn("source", "E")
	for _, id := range []string{"I1", "I2", "I3", "I4", "I5"} {
		h.approvedItem(id, 1)
	}
	for _, ws := range []string{"acme", "acme", "default", "default", "source"} {
		claim := h.claimIn(ws)
		h.toReview(claim.SliceID)
		h.ok(h.eng.PRApproved(ctx(), "github", "", claim.SliceID, "sha-"+claim.SliceID, "rev"))
	}
	if h.item("I1").Status != flow.ItemReleasing || h.item("I3").Status != flow.ItemReleasing || h.item("I5").Status != flow.ItemAwaitingRelease {
		t.Fatal("wrong release status")
	}
	h.ok(h.eng.PRMerged(ctx(), "github", "", "I1.1", ""))
	h.ok(h.eng.ReleaseDone(ctx(), "pat", "", "I1"))
	if h.item("I2").Status != flow.ItemReleasing || h.item("I4").Status != flow.ItemAwaitingRelease {
		t.Fatal("released slot crossed workstreams")
	}
	h.ok(h.eng.SetWorkstreamLimits(ctx(), "pat", "", "source", flow.PartialLimits{Release: intp(1)}))
	if h.item("I5").Status != flow.ItemReleasing {
		t.Fatal("raising release did not fill newly available slot")
	}
}

func TestWorkstreamLegacyReplay(t *testing.T) {
	h := newHarness(t, func(c *flow.Config) { c.Limits.Execute = 1 })
	h.appendLegacyLowTierItem(1, flow.EvReleaseSlotTaken)
	h.eng = h.reload()
	h.eng.View(func(s *flow.State) {
		if len(s.Workstreams) != 1 || s.Workstreams["default"].Limits != h.cfg.Limits.Stages() || s.Items["I1"].Workstream != "default" || s.Slices["I1.1"].Workstream != "default" {
			t.Fatal("legacy workstream replay failed")
		}
	})
	h.backlogIn("", "B", "C")
	h.approvedItem("I2", 1)
	h.approvedItem("I3", 1)
	h.claimIn("default")
	_, err := h.eng.Claim(ctx(), "pat", "", "laptop", "", "")
	wantCode(t, err, flow.CodeNoCapacity)
}

func TestWorkstreamCommandValidation(t *testing.T) {
	h := newHarness(t, nil)
	for _, name := range []string{"default", "bad/name"} {
		_, err := h.eng.CreateWorkstream(ctx(), "pat", "", name, flow.PartialLimits{})
		code := flow.CodeInvalid
		if name == "default" {
			code = flow.CodeConflict
		}
		wantCode(t, err, code)
	}
	_, err := h.eng.CreateWorkstream(ctx(), "pat", "", "acme", flow.PartialLimits{Execute: intp(-1)})
	wantCode(t, err, flow.CodeInvalid)
	_, err = h.eng.SetWorkstreamLimits(ctx(), "pat", "", "acme", flow.PartialLimits{Execute: intp(1)})
	wantCode(t, err, flow.CodeNotFound)
	_, err = h.eng.SyncBacklog(ctx(), "pat", "", "acme", nil)
	wantCode(t, err, flow.CodeNotFound)
	h.workstream("acme", flow.PartialLimits{Execute: intp(1)})
	result := h.ok(h.eng.SetWorkstreamLimits(ctx(), "pat", "", "acme", flow.PartialLimits{Execute: intp(1)}))
	if len(result.Events) != 0 {
		t.Fatal("no-op limit set produced events")
	}
}
