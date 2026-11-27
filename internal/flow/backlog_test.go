package flow_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jystringfellow/metronome/internal/flow"
)

func plannedBacklogItem() flow.BacklogItem {
	return flow.BacklogItem{
		Ref: "ACME-1", Title: "item 1", Owner: "pat", PredictedTier: intp(2),
		Slices: []flow.SliceSpec{
			{Title: "Export API", Repo: "acme/app", Criteria: []string{"Records can be saved"}, Approvers: []string{"rev"}},
			{Title: "Export page", Repo: "acme/web", After: []int{1}},
		},
	}
}

func (h *harness) inbox(owner string) []*flow.Flag {
	var flags []*flow.Flag
	h.eng.View(func(state *flow.State) { flags = state.Inbox(owner) })
	return flags
}

func wantEventTypes(t *testing.T, result flow.Result, want ...string) {
	t.Helper()
	var got []string
	for _, event := range result.Events {
		got = append(got, event.Type)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestBacklogWithoutSlicesStillNeedsAPlan(t *testing.T) {
	for _, specs := range [][]flow.SliceSpec{nil, {}} {
		h := newHarness(t, nil)
		item := plannedBacklogItem()
		item.Slices = specs
		h.ok(h.eng.SyncBacklog(ctx(), "pat", "", "", []flow.BacklogItem{item}))
		if got := h.item("I1"); got.PlannedSlices != nil || got.TierDefaulted || got.PredictedTier != 2 {
			t.Fatalf("item = %+v", got)
		}
		wantEventTypes(t, h.ok(h.eng.StartPlan(ctx(), "agent", "", "", "")), flow.EvPlanStarted)
		if got := h.item("I1"); got.Proposal != nil || len(h.inbox("pat")) != 0 {
			t.Fatalf("item = %+v, inbox = %+v", got, h.inbox("pat"))
		}
	}
}

func TestPrePlannedBacklogRequiresApproval(t *testing.T) {
	for _, test := range []struct{ name, workstream, itemID string }{
		{"automatic", "", ""},
		{"explicit", "", "I1"},
		{"workstream", "example", ""},
		{"explicit workstream", "example", "I1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, nil)
			if test.workstream != "" {
				h.workstream(test.workstream, flow.PartialLimits{})
			}
			item := plannedBacklogItem()
			wantEventTypes(t, h.ok(h.eng.SyncBacklog(ctx(), "pat", "", test.workstream, []flow.BacklogItem{item})), flow.EvItemAdded)
			if got := h.item("I1"); got.Status != flow.ItemReady || !reflect.DeepEqual(got.PlannedSlices, item.Slices) || got.Proposal != nil || len(h.inbox("pat")) != 0 {
				t.Fatalf("ready item = %+v", got)
			}
			changed := plannedBacklogItem()
			changed.Slices[0].Title = "Replacement"
			changed.PredictedTier = intp(3)
			if result := h.ok(h.eng.SyncBacklog(ctx(), "pat", "", test.workstream, []flow.BacklogItem{changed})); len(result.Events) != 0 {
				t.Fatalf("known ref emitted events: %+v", result.Events)
			}
			wantEventTypes(t, h.ok(h.eng.StartPlan(ctx(), "agent", "", test.workstream, test.itemID)), flow.EvPlanStarted, flow.EvPlanProposed, flow.EvFlagRaised)
			if got := h.item("I1"); got.Status != flow.ItemPlanning || got.Approver != "pat" || !reflect.DeepEqual(got.Proposal, item.Slices) || got.PredictedTier != 2 {
				t.Fatalf("proposed item = %+v", got)
			}
			inbox := h.inbox("pat")
			if len(inbox) != 1 || inbox[0].Ask != flow.AskApprove || !strings.Contains(inbox[0].Question, "Approve the plan for item 1 (2 slices)") || !strings.Contains(inbox[0].Question, "came from the backlog") {
				t.Fatalf("approval ask = %+v", inbox)
			}
			_, err := h.eng.ResolveFlag(ctx(), "pat", "", inbox[0].ID, "yes")
			wantCode(t, err, flow.CodeConflict)
			h.ok(h.eng.ApprovePlan(ctx(), "pat", "", "I1", nil))
			first, second := h.slice("I1.1"), h.slice("I1.2")
			for index, slice := range []flow.Slice{first, second} {
				spec := item.Slices[index]
				if slice.Title != spec.Title || slice.Repo != spec.Repo || !reflect.DeepEqual(slice.Criteria, spec.Criteria) || !reflect.DeepEqual(slice.Approvers, spec.Approvers) {
					t.Fatalf("slice = %+v, spec = %+v", slice, spec)
				}
			}
			if !reflect.DeepEqual(second.After, []string{"I1.1"}) || !reflect.DeepEqual(second.WaitsOn, second.After) {
				t.Fatalf("dependent slice = %+v", second)
			}
			if claim := h.claim("laptop"); claim.SliceID != "I1.1" {
				t.Fatalf("claim = %+v", claim)
			}
			beforeItem, beforeFirst, beforeSecond, beforeInbox := h.item("I1"), h.slice("I1.1"), h.slice("I1.2"), h.inbox("pat")
			h.eng = h.reload()
			if !reflect.DeepEqual(h.item("I1"), beforeItem) || !reflect.DeepEqual(h.slice("I1.1"), beforeFirst) || !reflect.DeepEqual(h.slice("I1.2"), beforeSecond) || !reflect.DeepEqual(h.inbox("pat"), beforeInbox) || len(beforeInbox) != 0 {
				t.Fatal("approved backlog plan changed on replay or left an open ask")
			}
		})
	}
}

func TestBacklogRejectsInvalidSlicesAtomically(t *testing.T) {
	for _, test := range []struct {
		name    string
		specs   []flow.SliceSpec
		message string
	}{
		{"missing title", []flow.SliceSpec{{Repo: "acme/app"}}, "slice 1 needs a title and a repo"},
		{"missing repo", []flow.SliceSpec{{Title: "example"}}, "slice 1 needs a title and a repo"},
		{"out of range", dependencySpecs(nil, []int{3}), "slice 2 lists after 3"},
		{"self", dependencySpecs(nil, []int{2}), "slice 2 lists itself"},
		{"repeated", dependencySpecs(nil, []int{1, 1}), "slice 2 lists 1 in after more than once"},
		{"cycle", dependencySpecs([]int{2}, []int{1}), "slices 1 and 2 wait on each other in a cycle"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, nil)
			bad := flow.BacklogItem{Ref: "ACME-2", Title: "item 2", Owner: "pat", Slices: test.specs}
			_, err := h.eng.SyncBacklog(ctx(), "pat", "", "", []flow.BacklogItem{plannedBacklogItem(), bad})
			wantCode(t, err, flow.CodeInvalid)
			if !strings.Contains(err.Error(), `backlog item "ACME-2": `+test.message) || len(h.eng.EventsAfter(0, 0)) != 0 || h.usage().Ready != 0 {
				t.Fatalf("error = %v, events = %+v", err, h.eng.EventsAfter(0, 0))
			}
		})
	}
	h := newHarness(t, nil)
	item := plannedBacklogItem()
	h.ok(h.eng.SyncBacklog(ctx(), "pat", "", "", []flow.BacklogItem{item}))
	item.Slices[0].Title = ""
	_, err := h.eng.SyncBacklog(ctx(), "pat", "", "", []flow.BacklogItem{item})
	wantCode(t, err, flow.CodeInvalid)
	if !strings.Contains(err.Error(), `backlog item "ACME-1"`) || len(h.eng.EventsAfter(0, 0)) != 1 {
		t.Fatalf("known ref error = %v, events = %+v", err, h.eng.EventsAfter(0, 0))
	}
}

func TestBacklogTierDefaulting(t *testing.T) {
	for _, test := range []struct {
		name                    string
		tier, override          *int
		wantTier, wantConfirmed int
		defaulted, handPlan     bool
	}{
		{"omitted", nil, nil, 1, 1, true, false},
		{"explicit zero", intp(0), nil, 0, 0, false, false},
		{"override", nil, intp(2), 1, 2, true, false},
		{"hand proposal", nil, nil, 1, 1, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, nil)
			item := plannedBacklogItem()
			item.PredictedTier = test.tier
			item.Slices = item.Slices[:1]
			specs := item.Slices
			if test.handPlan {
				item.Slices = nil
			}
			h.ok(h.eng.SyncBacklog(ctx(), "pat", "", "", []flow.BacklogItem{item}))
			if got := h.item("I1"); got.PredictedTier != test.wantTier || got.TierDefaulted != test.defaulted {
				t.Fatalf("item = %+v", got)
			}
			h.ok(h.eng.StartPlan(ctx(), "agent", "", "", "I1"))
			if test.handPlan {
				h.ok(h.eng.ProposePlan(ctx(), "agent", "", "I1", specs, ""))
			}
			inbox := h.inbox("pat")
			if len(inbox) != 1 || !strings.Contains(inbox[0].Question, fmt.Sprintf("confirm tier %d", test.wantTier)) || strings.Contains(inbox[0].Question, "no tier") != test.defaulted || strings.Contains(inbox[0].Question, "came from the backlog") == test.handPlan {
				t.Fatalf("approval ask = %+v", inbox)
			}
			h.ok(h.eng.ApprovePlan(ctx(), "pat", "", "I1", test.override))
			if got := h.item("I1"); got.ConfirmedTier != test.wantConfirmed || got.TierDefaulted != test.defaulted {
				t.Fatalf("approved item = %+v", got)
			}
			if test.wantConfirmed < 2 {
				h.claim("laptop")
				h.mergeDependency("I1.1")
				if got := h.item("I1"); got.Status != flow.ItemDone {
					t.Fatalf("merged item = %+v", got)
				}
			}
		})
	}
	for _, tier := range []int{-1, 4} {
		h := newHarness(t, nil)
		item := plannedBacklogItem()
		item.PredictedTier = intp(tier)
		_, err := h.eng.SyncBacklog(ctx(), "pat", "", "", []flow.BacklogItem{item})
		wantCode(t, err, flow.CodeInvalid)
		if !strings.Contains(err.Error(), fmt.Sprintf(`backlog item "ACME-1" has tier %d, want 0 to 3`, tier)) || len(h.eng.EventsAfter(0, 0)) != 0 {
			t.Fatalf("error = %v, events = %+v", err, h.eng.EventsAfter(0, 0))
		}
	}
}

func TestHandProposalSupersedesBacklogPlan(t *testing.T) {
	h := newHarness(t, nil)
	item := plannedBacklogItem()
	h.ok(h.eng.SyncBacklog(ctx(), "pat", "", "", []flow.BacklogItem{item}))
	h.ok(h.eng.StartPlan(ctx(), "agent", "", "", ""))
	old := h.inbox("pat")[0].ID
	specs := []flow.SliceSpec{{Title: "Replacement", Repo: "acme/app"}}
	wantEventTypes(t, h.ok(h.eng.ProposePlan(ctx(), "agent", "", "I1", specs, "")), flow.EvFlagResolved, flow.EvPlanProposed, flow.EvFlagRaised)
	h.eng.View(func(state *flow.State) {
		if flag := state.Flags[old]; !flag.Closed || flag.Answer != "superseded by a new proposal" {
			t.Fatalf("old flag = %+v", flag)
		}
	})
	if inbox := h.inbox("pat"); len(inbox) != 1 || inbox[0].ID == old || strings.Contains(inbox[0].Question, "came from the backlog") {
		t.Fatalf("replacement ask = %+v", inbox)
	}
	h.ok(h.eng.ApprovePlan(ctx(), "pat", "", "I1", nil))
	if got := h.item("I1"); !reflect.DeepEqual(got.Slices, []string{"I1.1"}) || !reflect.DeepEqual(got.PlannedSlices, item.Slices) || h.slice("I1.1").Title != specs[0].Title {
		t.Fatalf("approved item = %+v", got)
	}
}

func TestParkedBacklogPlanIsProposedAgain(t *testing.T) {
	h := newHarness(t, nil)
	item := plannedBacklogItem()
	h.ok(h.eng.SyncBacklog(ctx(), "pat", "", "", []flow.BacklogItem{item}))
	h.ok(h.eng.StartPlan(ctx(), "agent", "", "", ""))
	h.ok(h.eng.RaiseFlag(ctx(), "agent", "", flow.Subject{Kind: flow.SubjectItem, ID: "I1"}, flow.AskAnswer, "Which format?", ""))
	h.clock.advance(h.cfg.AskTimeout)
	h.ok(h.eng.Tick(ctx()))
	if got := h.item("I1"); got.Status != flow.ItemReady || got.Proposal != nil || len(h.inbox("pat")) != 0 {
		t.Fatalf("parked item = %+v", got)
	}
	wantEventTypes(t, h.ok(h.eng.StartPlan(ctx(), "agent", "", "", "")), flow.EvPlanStarted, flow.EvPlanProposed, flow.EvFlagRaised)
	if got := h.item("I1"); !reflect.DeepEqual(got.Proposal, item.Slices) || len(h.inbox("pat")) != 1 {
		t.Fatalf("reproposed item = %+v", got)
	}
}

func TestBacklogPlanReplayDoesNotDeriveProposals(t *testing.T) {
	for _, test := range []struct {
		name, payload string
		wantTier      int
		wantDefault   bool
		wantSlices    []flow.SliceSpec
	}{
		{"legacy", `{"item_id":"I1","ref":"ACME-1","title":"example","owner":"pat","predicted_tier":0,"rank":1}`, 0, false, nil},
		{"recorded slices", `{"item_id":"I1","ref":"ACME-1","title":"example","owner":"pat","predicted_tier":1,"tier_defaulted":true,"rank":1,"slices":[{"title":"first","repo":"acme/app"}]}`, 1, true, []flow.SliceSpec{{Title: "first", Repo: "acme/app"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, nil)
			_, err := h.store.Append(ctx(), []flow.Event{
				{ID: "added", Type: flow.EvItemAdded, At: h.clock.Now(), Actor: "pat", Data: json.RawMessage(test.payload)},
				{ID: "started", Type: flow.EvPlanStarted, At: h.clock.Now(), Actor: "pat", Data: json.RawMessage(`{"item_id":"I1"}`)},
			})
			if err != nil {
				t.Fatal(err)
			}
			h.eng = h.reload()
			if got := h.item("I1"); got.Status != flow.ItemPlanning || got.PredictedTier != test.wantTier || got.TierDefaulted != test.wantDefault || !reflect.DeepEqual(got.PlannedSlices, test.wantSlices) || got.Proposal != nil || len(h.inbox("pat")) != 0 {
				t.Fatalf("replayed item = %+v", got)
			}
			h.ok(h.eng.ProposePlan(ctx(), "agent", "", "I1", dependencySpecs(nil), ""))
			if inbox := h.inbox("pat"); len(inbox) != 1 || strings.Contains(inbox[0].Question, "no tier") != test.wantDefault {
				t.Fatalf("approval ask = %+v", inbox)
			}
		})
	}
}
