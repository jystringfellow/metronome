package flow_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jystringfellow/metronome/internal/flow"
)

func adoption() flow.Adoption {
	return flow.Adoption{Repo: "acme/app", Number: 12, HeadRepo: "acme/app", Branch: "feature-x", HeadSHA: "abc", Author: "pat", Title: "Export"}
}

func TestAdoptPR(t *testing.T) {
	h := newHarness(t, nil)
	h.ok(h.eng.CreateWorkstream(ctx(), "pat", "", "example", flow.PartialLimits{}))
	h.ok(h.eng.SetAdoptLabel(ctx(), "pat", "", "example", " metronome:adopt "))
	req := adoption()
	req.Workstream = "example"
	result := h.ok(h.eng.AdoptPR(ctx(), "github", "adopt", req))
	if len(result.Events) != 1 || result.Events[0].Type != flow.EvPRAdopted {
		t.Fatal(result)
	}
	it, sl := h.item("I1"), h.slice("I1.1")
	if it.Status != flow.ItemExecuting || it.Owner != "pat" || it.Workstream != "example" || it.Ref != "acme/app#12" || it.ConfirmedTier != 1 || !it.TierDefaulted {
		t.Fatal(it)
	}
	if sl.Status != flow.SlicePROpen || !sl.HoldsReview || sl.HoldsExecute || sl.Branch != req.Branch || sl.PR.HeadSHA != req.HeadSHA || sl.Adopted.Author != req.Author {
		t.Fatal(sl)
	}
	if usage := h.usage(); usage.Review != 1 || usage.Execute != 0 || usage.Ready != 0 {
		t.Fatal(usage)
	}
	transitions, _ := h.eng.Transitions("example", 0, 100)
	if len(transitions) != 2 || transitions[0].Transition != "adopted" || transitions[1].Transition != "executing" {
		t.Fatal(transitions)
	}
	h.eng = h.reload()
	if !reflect.DeepEqual(it, h.item("I1")) || !reflect.DeepEqual(sl, h.slice("I1.1")) {
		t.Fatal("replay differs")
	}
	req.Repo = "ACME/APP"
	result = h.ok(h.eng.AdoptPR(ctx(), "github", "", req))
	if len(result.Events) != 0 || !strings.Contains(strings.Join(result.Notes, " "), "already followed") {
		t.Fatal(result)
	}
	if !h.ok(h.eng.AdoptPR(ctx(), "github", "adopt", req)).Duplicate {
		t.Fatal("expected duplicate")
	}
}

func TestAdoptRefusesForkAndFullReview(t *testing.T) {
	h := newHarness(t, nil)
	req := adoption()
	req.HeadRepo = "other/app"
	_, err := h.eng.AdoptPR(ctx(), "github", "", req)
	wantCode(t, err, flow.CodeInvalid)
	if !strings.Contains(err.Error(), "fork") || h.usage().Review != 0 {
		t.Fatal(err)
	}
	h.ok(h.eng.SetWorkstreamLimits(ctx(), "pat", "", "default", flow.PartialLimits{Review: intp(1)}))
	req = adoption()
	h.ok(h.eng.AdoptPR(ctx(), "github", "", req))
	req.Number++
	req.Branch = "feature-y"
	_, err = h.eng.AdoptPR(ctx(), "github", "", req)
	wantCode(t, err, flow.CodeNoCapacity)
}

func TestAdoptSourceAndRework(t *testing.T) {
	h := newHarness(t, nil)
	h.ok(h.eng.IntakeBacklog(ctx(), "pat", "", "default", []flow.BacklogItem{{Ref: "ACME-1", Title: "Export", Owner: "sam", PRs: []string{"ACME/app#12", "acme/app#12"}, Slices: []flow.SliceSpec{{Title: "Export data", Repo: "acme/other", Criteria: []string{"CSV"}}}}}))
	_, err := h.eng.StartPlan(ctx(), "pat", "", "default", "I1")
	wantCode(t, err, flow.CodeConflict)
	_, err = h.eng.StartPlan(ctx(), "pat", "", "default", "")
	wantCode(t, err, flow.CodeNoWork)
	req := adoption()
	req.ItemID = "I1"
	h.ok(h.eng.AdoptPR(ctx(), "github", "", req))
	if it := h.item("I1"); it.Owner != "sam" || !reflect.DeepEqual(it.PRs, []string{"acme/app#12"}) {
		t.Fatal(it)
	}
	if sl := h.slice("I1.1"); sl.Title != "Export data" || sl.Repo != "acme/app" || sl.Adopted.Author != "pat" {
		t.Fatal(sl)
	}
	if sl := h.slice("I1.1"); sl.Approvers == nil {
		t.Fatal("expected empty approvers array")
	}
	transitions, _ := h.eng.Transitions("default", 0, 100)
	if len(transitions) != 2 || transitions[0].Transition != "adopted" || transitions[1].Transition != "executing" || transitions[1].From != "ready" {
		t.Fatal(transitions)
	}
	h.ok(h.eng.ChecksCompleted(ctx(), "github", "", "I1.1", "abc", true))
	h.ok(h.eng.ReadyForReview(ctx(), "github", "", "I1.1"))
	h.ok(h.eng.ChecksCompleted(ctx(), "github", "", "I1.1", "abc", false))
	if sl := h.slice("I1.1"); sl.Status != flow.SliceStalled || !sl.Rework || !sl.HoldsReview || sl.HoldsExecute {
		t.Fatal(sl)
	}
	claim := h.claim("example")
	_, err = h.eng.Submit(ctx(), "agent", "", claim.RunID, "different")
	wantCode(t, err, flow.CodeConflict)
	h.ok(h.eng.Submit(ctx(), "agent", "", claim.RunID, req.Branch))
	h.ok(h.eng.PROpened(ctx(), "github", "", "I1.1", req.Repo, req.Number, "def"))
	h.reapprove("I1.1", "def")
	if sl := h.slice("I1.1"); sl.Status != flow.SliceApproved || sl.PR.ApprovedSHA != "def" || sl.HoldsExecute {
		t.Fatal(sl)
	}
	result := h.ok(h.eng.PROpened(ctx(), "github", "", "I1.1", req.Repo, req.Number, "ghi"))
	wantEventTypes(t, result, flow.EvPROpened, flow.EvApprovalWithdrawn)
	if sl := h.slice("I1.1"); sl.Status != flow.SlicePROpen || sl.PR.ApprovedSHA != "" || !sl.HoldsReview {
		t.Fatal(sl)
	}
	h.reapprove("I1.1", "ghi")
	h.ok(h.eng.PRMerged(ctx(), "github", "", "I1.1", "pat"))
	if sl := h.slice("I1.1"); sl.Status != flow.SliceMerged || sl.HoldsExecute || sl.HoldsReview || sl.Adopted.Branch != req.Branch {
		t.Fatal(sl)
	}
	h.wantDone("I1")
	if usage := h.usage(); usage.Ready != 0 || usage.Plan != 0 || usage.Execute != 0 || usage.Review != 0 || usage.Release != 0 {
		t.Fatal(usage)
	}
}

func TestAdoptConflicts(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*harness, *flow.Adoption)
		code  flow.ErrorCode
		text  string
	}{
		{"branch", func(h *harness, req *flow.Adoption) { h.ok(h.eng.AdoptPR(ctx(), "github", "", *req)); req.Number++ }, flow.CodeConflict, "branch"},
		{"ref", func(h *harness, req *flow.Adoption) {
			h.intake(flow.BacklogItem{Ref: "acme/app#12", Title: "Export", Owner: "pat"})
		}, flow.CodeConflict, "already has ref"},
		{"workstream", func(h *harness, req *flow.Adoption) { req.Workstream = "example" }, flow.CodeNotFound, "workstream"},
		{"not listed", func(h *harness, req *flow.Adoption) { h.intake(sourceItems(1)...); req.ItemID = "I1" }, flow.CodeConflict, "must be ready and name"},
		{"not ready", func(h *harness, req *flow.Adoption) {
			h.intake(flow.BacklogItem{Ref: "ACME-1", Title: "Export", Owner: "pat", PRs: []string{"acme/app#12"}})
			h.intake()
			req.ItemID = "I1"
		}, flow.CodeConflict, "must be ready"},
		{"source workstream", func(h *harness, req *flow.Adoption) {
			h.intake(flow.BacklogItem{Ref: "ACME-1", Title: "Export", Owner: "pat", PRs: []string{"acme/app#12"}})
			h.workstream("example", flow.PartialLimits{})
			req.ItemID = "I1"
			req.Workstream = "example"
		}, flow.CodeConflict, "in workstream"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, nil)
			req := adoption()
			test.setup(h, &req)
			before := h.eng.EventsAfter(0, 0)
			_, err := h.eng.AdoptPR(ctx(), "github", "", req)
			wantCode(t, err, test.code)
			if !strings.Contains(err.Error(), test.text) || !reflect.DeepEqual(before, h.eng.EventsAfter(0, 0)) {
				t.Fatal(err)
			}
		})
	}
}

func TestAdoptSourceEmptyArrays(t *testing.T) {
	h := newHarness(t, nil)
	h.intake(flow.BacklogItem{Ref: "ACME-1", Title: "Export", Owner: "pat", PRs: []string{"acme/app#12"}, Slices: []flow.SliceSpec{{Title: "Export", Repo: "acme/app"}}})
	req := adoption()
	req.ItemID = "I1"
	result := h.ok(h.eng.AdoptPR(ctx(), "github", "", req))
	if data := string(result.Events[0].Data); !strings.Contains(data, `"criteria":[]`) || !strings.Contains(data, `"approvers":[]`) {
		t.Fatal(data)
	}
	h.eng = h.reload()
	if sl := h.slice("I1.1"); sl.Criteria == nil || sl.Approvers == nil {
		t.Fatal(sl)
	}
}

func TestBacklogPRValidation(t *testing.T) {
	var many []string
	for number := 1; number <= 11; number++ {
		many = append(many, fmt.Sprintf("acme/app#%d", number))
	}
	for _, test := range []struct {
		prs   []string
		specs []flow.SliceSpec
	}{
		{prs: []string{"acme/app#0"}}, {prs: many},
		{prs: []string{"acme/app#12"}, specs: []flow.SliceSpec{{Title: "One", Repo: "acme/app"}, {Title: "Two", Repo: "acme/app"}}},
		{prs: []string{"acme/app#12"}, specs: []flow.SliceSpec{{Title: "One", Repo: "acme/app", After: []int{1}}}},
	} {
		h := newHarness(t, nil)
		items := []flow.BacklogItem{{Ref: "ACME-1", Title: "Export", Owner: "pat", PRs: test.prs, Slices: test.specs}}
		_, err := h.eng.SyncBacklog(ctx(), "pat", "", "default", items)
		wantCode(t, err, flow.CodeInvalid)
		_, err = h.eng.IntakeBacklog(ctx(), "pat", "", "default", items)
		wantCode(t, err, flow.CodeInvalid)
		if h.usage().Ready != 0 {
			t.Fatal(h.usage())
		}
	}
}

func TestSetAdoptLabel(t *testing.T) {
	h := newHarness(t, nil)
	h.workstream("example", flow.PartialLimits{})
	for _, test := range []struct {
		ws, label string
		code      flow.ErrorCode
	}{{"default", "a,b", flow.CodeInvalid}, {"default", strings.Repeat("é", 51), flow.CodeInvalid}, {"missing", "adopt", flow.CodeNotFound}} {
		_, err := h.eng.SetAdoptLabel(ctx(), "pat", "", test.ws, test.label)
		wantCode(t, err, test.code)
	}
	wantEventTypes(t, h.ok(h.eng.SetAdoptLabel(ctx(), "pat", "", "default", " Adopt ")), flow.EvWorkstreamAdoptLabelSet)
	wantEventTypes(t, h.ok(h.eng.SetAdoptLabel(ctx(), "pat", "", "default", "Adopt")))
	_, err := h.eng.SetAdoptLabel(ctx(), "pat", "", "example", "aDOPT")
	wantCode(t, err, flow.CodeConflict)
	h.eng = h.reload()
	h.eng.View(func(s *flow.State) {
		if s.Workstreams["default"].AdoptLabel != "Adopt" {
			t.Fatal(s.WorkstreamViews())
		}
	})
	wantEventTypes(t, h.ok(h.eng.SetAdoptLabel(ctx(), "pat", "", "default", "")), flow.EvWorkstreamAdoptLabelSet)
	h.eng = h.reload()
	h.eng.View(func(s *flow.State) {
		if s.Workstreams["default"].AdoptLabel != "" {
			t.Fatal(s.WorkstreamViews())
		}
	})
	wantEventTypes(t, h.ok(h.eng.SetAdoptLabel(ctx(), "pat", "", "example", "adopt")), flow.EvWorkstreamAdoptLabelSet)
}
