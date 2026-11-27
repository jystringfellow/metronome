package flow_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jystringfellow/metronome/internal/flow"
)

func dependencySpecs(after ...[]int) []flow.SliceSpec {
	var specs []flow.SliceSpec
	for index, positions := range after {
		specs = append(specs, flow.SliceSpec{Title: fmt.Sprintf("slice %d", index+1), Repo: "acme/app", Approvers: []string{"rev"}, After: positions})
	}
	return specs
}

func (h *harness) planWith(itemID string, specs []flow.SliceSpec) {
	h.t.Helper()
	h.ok(h.eng.StartPlan(ctx(), "agent", "", "", itemID))
	h.ok(h.eng.ProposePlan(ctx(), "agent", "", itemID, specs, ""))
	h.ok(h.eng.ApprovePlan(ctx(), "pat", "", itemID, nil))
}

func (h *harness) mergeDependency(id string) {
	h.t.Helper()
	h.toReview(id)
	h.ok(h.eng.PRApproved(ctx(), "github", "", id, "sha-"+id, "rev"))
	h.ok(h.eng.PRMerged(ctx(), "github", "", id, ""))
}

func TestASliceWaitsUntilItsDependenciesMerge(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.planWith("I1", dependencySpecs(nil, []int{1}, nil))
	for _, id := range []string{"I1.1", "I1.3"} {
		if claim := h.claim("laptop"); claim.SliceID != id {
			t.Fatalf("claim = %+v, want %s", claim, id)
		}
	}
	_, err := h.eng.Claim(ctx(), "laptop", "", "laptop", "", "")
	wantCode(t, err, flow.CodeNoWork)
	if !strings.Contains(err.Error(), "I1.2 waits on I1.1") {
		t.Fatal(err)
	}
	waiting := h.slice("I1.2")
	if !reflect.DeepEqual(waiting.After, []string{"I1.1"}) || !reflect.DeepEqual(waiting.WaitsOn, waiting.After) || waiting.Status != flow.SliceQueued || waiting.HoldsExecute || waiting.HoldsReview {
		t.Fatalf("waiting = %+v", waiting)
	}
	h.eng = h.reload()
	if got := h.slice("I1.2").WaitsOn; !reflect.DeepEqual(got, waiting.WaitsOn) {
		t.Fatalf("replayed waits_on = %v", got)
	}
	h.mergeDependency("I1.1")
	h.eng = h.reload()
	before := len(h.eng.EventsAfter(0, 0))
	if claim := h.claim("laptop"); claim.SliceID != "I1.2" || len(h.slice("I1.2").WaitsOn) != 0 {
		t.Fatalf("claim = %+v", claim)
	}
	if len(h.eng.EventsAfter(0, 0)) != before+1 {
		t.Fatal("claim appended more than one event")
	}
}

func TestClaimOrderSkipsWaitingSlicesAcrossItems(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(2)
	h.planWith("I1", dependencySpecs([]int{2}, nil))
	h.approvedItem("I2", 1)
	for _, id := range []string{"I1.2", "I2.1"} {
		if claim := h.claim("laptop"); claim.SliceID != id {
			t.Fatalf("claim = %+v, want %s", claim, id)
		}
	}
	h.mergeDependency("I1.2")
	if claim := h.claim("laptop"); claim.SliceID != "I1.1" {
		t.Fatalf("claim = %+v", claim)
	}
}

func TestClaimingAWaitingSliceByIDIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.planWith("I1", dependencySpecs(nil, nil, []int{1, 2}))
	check := func(message string) {
		t.Helper()
		before := len(h.eng.EventsAfter(0, 0))
		_, err := h.eng.Claim(ctx(), "laptop", "", "laptop", "", "I1.3")
		wantCode(t, err, flow.CodeConflict)
		if !strings.Contains(err.Error(), message) || len(h.eng.EventsAfter(0, 0)) != before {
			t.Fatalf("claim error = %v, want %q without new events", err, message)
		}
	}
	check("I1.1 (queued) and I1.2 (queued)")
	h.claim("laptop")
	check("I1.1 (running) and I1.2 (queued)")
	h.mergeDependency("I1.1")
	check("waits on I1.2 (queued)")
	h.claim("laptop")
	h.mergeDependency("I1.2")
	result := h.ok(h.eng.Claim(ctx(), "laptop", "", "laptop", "", "I1.3"))
	if claim, ok := flow.ClaimFrom(result); !ok || claim.SliceID != "I1.3" {
		t.Fatalf("claim = %+v", claim)
	}
}

func TestAPlanWithInvalidDependenciesIsRefused(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.ok(h.eng.StartPlan(ctx(), "agent", "", "", "I1"))
	for _, test := range []struct {
		name    string
		after   [][]int
		message string
	}{
		{"zero", [][]int{{0}, nil}, "slice 1 lists after 0"},
		{"negative", [][]int{{-1}, nil}, "slice 1 lists after -1"},
		{"too large", [][]int{nil, {3}}, "slice 2 lists after 3"},
		{"self", [][]int{nil, {2}}, "slice 2 lists itself"},
		{"duplicate", [][]int{nil, {1, 1}}, "slice 2 lists 1 in after more than once"},
		{"two-node cycle", [][]int{{2}, {1}}, "cycle"},
		{"three-node cycle", [][]int{{2}, {3}, {1}}, "cycle"},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := len(h.eng.EventsAfter(0, 0))
			_, err := h.eng.ProposePlan(ctx(), "agent", "", "I1", dependencySpecs(test.after...), "")
			wantCode(t, err, flow.CodeInvalid)
			if !strings.Contains(err.Error(), test.message) || h.item("I1").Proposal != nil || len(h.eng.EventsAfter(0, 0)) != before {
				t.Fatalf("invalid plan error = %v, item = %+v", err, h.item("I1"))
			}
		})
	}
	h.ok(h.eng.ProposePlan(ctx(), "agent", "", "I1", dependencySpecs(nil, nil, []int{1, 2}, []int{3}), ""))
}

func TestLogsWithoutDependenciesReplayUnchanged(t *testing.T) {
	h := newHarness(t, nil)
	var events []flow.Event
	for index, payload := range []struct{ typ, data string }{
		{flow.EvItemAdded, `{"item_id":"I1","ref":"ACME-1","title":"example","owner":"pat","predicted_tier":1,"rank":1}`},
		{flow.EvPlanStarted, `{"item_id":"I1"}`},
		{flow.EvPlanProposed, `{"item_id":"I1","slices":[{"title":"first","repo":"acme/app"},{"title":"second","repo":"acme/app"}],"approver":"pat"}`},
		{flow.EvPlanApproved, `{"item_id":"I1","confirmed_tier":1,"slice_ids":["I1.1","I1.2"]}`},
	} {
		events = append(events, flow.Event{ID: fmt.Sprintf("legacy-%d", index), Type: payload.typ, At: h.clock.Now(), Actor: "pat", Data: json.RawMessage(payload.data)})
	}
	if _, err := h.store.Append(ctx(), events); err != nil {
		t.Fatal(err)
	}
	h.eng = h.reload()
	for _, id := range []string{"I1.1", "I1.2"} {
		sl := h.slice(id)
		if sl.After != nil || sl.WaitsOn != nil {
			t.Fatalf("legacy slice = %+v", sl)
		}
		if claim := h.claim("laptop"); claim.SliceID != id {
			t.Fatalf("claim = %+v, want %s", claim, id)
		}
	}
}

func TestDependenciesReportStatus(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.planWith("I1", dependencySpecs(nil, nil, []int{2, 1}))
	want := []flow.Dependency{{SliceID: "I1.2", Title: "slice 2", Status: flow.SliceQueued}, {SliceID: "I1.1", Title: "slice 1", Status: flow.SliceQueued}}
	check := func() {
		t.Helper()
		h.eng.View(func(s *flow.State) {
			if got := s.Dependencies("I1.3"); !reflect.DeepEqual(got, want) {
				t.Fatalf("dependencies = %+v, want %+v", got, want)
			}
		})
	}
	check()
	h.ok(h.eng.Claim(ctx(), "laptop", "", "laptop", "", "I1.2"))
	want[0].Status = flow.SliceRunning
	check()
	previous := h.slice("I1.3")
	h.mergeDependency("I1.2")
	want[0].Status = flow.SliceMerged
	check()
	if !reflect.DeepEqual(previous.WaitsOn, []string{"I1.2", "I1.1"}) || !reflect.DeepEqual(h.slice("I1.3").WaitsOn, []string{"I1.1"}) {
		t.Fatal("merge did not preserve the previous board snapshot and remove the merged dependency")
	}
}

func TestNoWorkReportsWaitingSlicesInTheRequestedWorkstreamOnly(t *testing.T) {
	h := newHarness(t, nil)
	h.workstream("example", flow.PartialLimits{})
	h.backlogIn("example", "ACME-1")
	h.backlogIn("", "ACME-2")
	for _, id := range []string{"I1", "I2"} {
		h.planWith(id, dependencySpecs(nil, []int{1}))
	}
	h.claimIn("example")
	h.claimIn("default")
	_, err := h.eng.Claim(ctx(), "laptop", "", "laptop", "example", "")
	wantCode(t, err, flow.CodeNoWork)
	if !strings.Contains(err.Error(), "I1.2 waits on I1.1") || strings.Contains(err.Error(), "I2.2") {
		t.Fatal(err)
	}
}
