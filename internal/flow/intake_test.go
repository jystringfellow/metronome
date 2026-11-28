package flow_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jystringfellow/metronome/internal/flow"
)

func sourceItems(numbers ...int) []flow.BacklogItem {
	items := make([]flow.BacklogItem, 0, len(numbers))
	for _, number := range numbers {
		items = append(items, flow.BacklogItem{Ref: fmt.Sprintf("ACME-%d", number), Title: fmt.Sprintf("item %d", number), Owner: "pat"})
	}
	return items
}

func (h *harness) intake(items ...flow.BacklogItem) flow.Result {
	h.t.Helper()
	return h.ok(h.eng.IntakeBacklog(ctx(), "pat", "", "default", items))
}

func wantNote(t *testing.T, result flow.Result, text string) {
	t.Helper()
	if !strings.Contains(strings.Join(result.Notes, "\n"), text) {
		t.Fatalf("notes %v do not contain %q", result.Notes, text)
	}
}

func TestIntakeAddsNewItemsWithinReadyLimits(t *testing.T) {
	h := newHarness(t, nil)
	h.workstream("example", flow.PartialLimits{Ready: intp(3)})
	result := h.ok(h.eng.IntakeBacklog(ctx(), "pat", "", "example", sourceItems(1, 2, 3, 4, 5)))
	wantEventTypes(t, result, flow.EvItemAdded, flow.EvItemAdded, flow.EvItemAdded)
	wantNote(t, result, "2 source items wait for Ready room")
	for index := 1; index <= 3; index++ {
		if it := h.item(fmt.Sprintf("I%d", index)); it.Rank != index || it.Workstream != "example" || !it.TierDefaulted {
			t.Fatal(it)
		}
	}
	h.eng.View(func(s *flow.State) {
		if s.StageUsage("default").Ready != 0 || s.StageUsage("example").Ready != 3 {
			t.Fatal(s.Usage())
		}
	})
	wantEventTypes(t, h.ok(h.eng.IntakeBacklog(ctx(), "pat", "", "example", sourceItems(1, 2, 3, 4, 5))))
}

func TestIntakeReranksAndUpdatesReadyItems(t *testing.T) {
	h := newHarness(t, nil)
	h.intake(sourceItems(1, 2, 3)...)
	items := sourceItems(2, 1, 3)
	items[0].Title, items[1].Intent = "new title", "new intent"
	items[2].Owner = "example"
	result := h.intake(items...)
	wantEventTypes(t, result, flow.EvItemUpdated, flow.EvItemUpdated)
	wantNote(t, result, "intake updates only rank, title, intent and labels")
	if it := h.item("I1"); it.Rank != 2 || it.Intent != "new intent" {
		t.Fatal(it)
	}
	if it := h.item("I2"); it.Rank != 1 || it.Title != "new title" {
		t.Fatal(it)
	}
	if it := h.item("I3"); it.Owner != "pat" {
		t.Fatal(it)
	}
	h.ok(h.eng.StartPlan(ctx(), "pat", "", "", ""))
	if h.item("I2").Status != flow.ItemPlanning {
		t.Fatal("wrong priority")
	}
}

func TestIntakeShelvesAndRestoresReadyItems(t *testing.T) {
	h := newHarness(t, nil)
	h.ok(h.eng.SetWorkstreamLimits(ctx(), "pat", "", "default", flow.PartialLimits{Ready: intp(2)}))
	h.intake(sourceItems(1, 2, 3)...)
	wantEventTypes(t, h.intake(sourceItems(2, 3)...), flow.EvItemShelved, flow.EvItemUpdated, flow.EvItemAdded)
	if it := h.item("I1"); it.Status != flow.ItemShelved || it.ShelvedReason != "missing" || h.usage().Ready != 2 {
		t.Fatal(it)
	}
	_, err := h.eng.StartPlan(ctx(), "pat", "", "", "I1")
	wantCode(t, err, flow.CodeConflict)
	items := sourceItems(1, 2)
	items[0].Title = "returned"
	wantEventTypes(t, h.intake(items...), flow.EvItemUpdated, flow.EvItemShelved, flow.EvItemRestored)
	if it := h.item("I1"); it.Status != flow.ItemReady || it.Title != "returned" || it.Rank != 1 || it.ShelvedReason != "" {
		t.Fatal(it)
	}
}

func TestIntakeNeverDropsItemsInPlanOrLater(t *testing.T) {
	h := newHarness(t, nil)
	h.intake(sourceItems(1, 2)...)
	h.approvedItem("I2", 1)
	h.claim("laptop")
	h.ok(h.eng.StartPlan(ctx(), "pat", "", "", "I1"))
	first, second := h.item("I1"), h.item("I2")
	items := sourceItems(2, 1)
	items[0].Title, items[1].Title = "changed", "changed"
	wantEventTypes(t, h.intake(items...))
	result := h.intake()
	wantEventTypes(t, result)
	wantNote(t, result, "ACME-1 (I1, planning) is no longer in the source")
	wantNote(t, result, "ACME-2 (I2, executing) is no longer in the source")
	if !reflect.DeepEqual(first, h.item("I1")) || !reflect.DeepEqual(second, h.item("I2")) {
		t.Fatal("authored items changed")
	}
}

func TestIntakeRespectsClaimsInTheSource(t *testing.T) {
	h := newHarness(t, nil)
	items := sourceItems(1, 2)
	items[1].ClaimedBy = "laptop-b"
	result := h.intake(items...)
	wantEventTypes(t, result, flow.EvItemAdded)
	wantNote(t, result, "ACME-2 is claimed by laptop-b")
	items[0].ClaimedBy = "laptop-b"
	wantEventTypes(t, h.intake(items...), flow.EvItemShelved)
	if it := h.item("I1"); it.Status != flow.ItemShelved || it.ShelvedReason != "claimed" || it.ClaimedBy != "laptop-b" {
		t.Fatal(it)
	}
	wantEventTypes(t, h.intake(items...))
	items[0].ClaimedBy = ""
	wantEventTypes(t, h.intake(items...), flow.EvItemRestored)
	if it := h.item("I1"); it.ClaimedBy != "" || it.ShelvedReason != "" {
		t.Fatal(it)
	}
	h.ok(h.eng.StartPlan(ctx(), "pat", "", "", "I1"))
	items[0].ClaimedBy = "laptop-b"
	result = h.intake(items...)
	wantEventTypes(t, result)
	wantNote(t, result, "ACME-1 (I1, planning) is claimed by laptop-b")
	result = h.ok(h.eng.SyncBacklog(ctx(), "pat", "", "", items))
	wantEventTypes(t, result)
	wantNote(t, result, "ACME-2 is claimed by laptop-b")
}

func TestIntakeIsIdempotentAndReplays(t *testing.T) {
	h := newHarness(t, nil)
	initial := sourceItems(1, 2, 3)
	initial[0].Labels = []string{"old"}
	initial[2].Labels = []string{"shelved"}
	h.intake(initial...)
	h.intake(sourceItems(1, 2)...)
	items := sourceItems(3, 1, 4)
	items[1].Intent = "updated"
	items[0].Labels = []string{"Report", "slow"}
	items[1].Labels = []string{"business"}
	items[2].Labels = []string{"new"}
	wantEventTypes(t, h.intake(items...), flow.EvItemUpdated, flow.EvItemShelved, flow.EvItemRestored, flow.EvItemAdded)
	before := h.eng.EventsAfter(0, 0)
	wantEventTypes(t, h.intake(items...))
	if !reflect.DeepEqual(before, h.eng.EventsAfter(0, 0)) {
		t.Fatal("repeat changed log")
	}
	var state []flow.Item
	for index := 1; index <= 4; index++ {
		state = append(state, h.item(fmt.Sprintf("I%d", index)))
	}
	h.eng = h.reload()
	for index, it := range state {
		if !reflect.DeepEqual(it, h.item(fmt.Sprintf("I%d", index+1))) {
			t.Fatal("replay changed item", it)
		}
	}
}

func TestIntakeRefusesInvalidSourcesAtomically(t *testing.T) {
	h := newHarness(t, nil)
	h.intake(sourceItems(1)...)
	before := h.eng.EventsAfter(0, 0)
	for _, items := range [][]flow.BacklogItem{
		sourceItems(2, 2),
		{{Ref: "ACME-2", Title: "missing owner"}},
		{{Ref: "ACME-2", Title: "bad tier", Owner: "pat", PredictedTier: intp(4)}},
		{{Ref: "ACME-2", Title: "bad slices", Owner: "pat", Slices: []flow.SliceSpec{{Title: "missing repo"}}}},
	} {
		result, err := h.eng.IntakeBacklog(ctx(), "pat", "", "default", items)
		wantCode(t, err, flow.CodeInvalid)
		if len(result.Events) != 0 || !reflect.DeepEqual(before, h.eng.EventsAfter(0, 0)) {
			t.Fatal("invalid source changed state")
		}
	}
	_, err := h.eng.IntakeBacklog(ctx(), "pat", "", "missing", sourceItems(2))
	wantCode(t, err, flow.CodeNotFound)
}

func TestIntakeUpdatesAndRestoresPRReferences(t *testing.T) {
	h := newHarness(t, nil)
	items := sourceItems(1)
	h.intake(items...)
	items[0].PRs = []string{"ACME/app#12", "acme/app#12"}
	wantEventTypes(t, h.intake(items...), flow.EvItemUpdated)
	if got := h.item("I1").PRs; !reflect.DeepEqual(got, []string{"acme/app#12"}) {
		t.Fatal(got)
	}
	wantEventTypes(t, h.intake(items...))
	h.intake()
	items[0].PRs = []string{"acme/app#13"}
	wantEventTypes(t, h.intake(items...), flow.EvItemRestored)
	h.eng = h.reload()
	if got := h.item("I1").PRs; !reflect.DeepEqual(got, items[0].PRs) {
		t.Fatal(got)
	}
	items[0].PRs = nil
	wantEventTypes(t, h.intake(items...), flow.EvItemUpdated)
	if len(h.item("I1").PRs) != 0 {
		t.Fatal(h.item("I1"))
	}
}

func TestIntakePRDifferencesOnlyForActiveItems(t *testing.T) {
	h := newHarness(t, nil)
	items := sourceItems(1)
	h.intake(items...)
	h.ok(h.eng.StartPlan(ctx(), "pat", "", "default", "I1"))
	items[0].PRs = []string{"acme/app#12"}
	check := func(want string) {
		t.Helper()
		for _, result := range []flow.Result{h.intake(items...), h.ok(h.eng.SyncBacklog(ctx(), "pat", "", "default", items))} {
			if want == "" {
				if len(result.Notes) != 0 {
					t.Fatal(result)
				}
			} else {
				wantNote(t, result, want)
			}
		}
	}
	check("the source names PRs acme/app#12")
	h.ok(h.eng.DropPlan(ctx(), "pat", "", "I1"))
	check("")
	h = newHarness(t, nil)
	h.intake(items...)
	req := adoption()
	req.ItemID = "I1"
	h.ok(h.eng.AdoptPR(ctx(), "github", "", req))
	items[0].PRs = nil
	check("the source names PRs none")
	h.ok(h.eng.ChecksCompleted(ctx(), "github", "", "I1.1", "abc", true))
	h.ok(h.eng.ReadyForReview(ctx(), "github", "", "I1.1"))
	h.ok(h.eng.PRMerged(ctx(), "github", "", "I1.1", "pat"))
	if h.item("I1").Status != flow.ItemDone {
		t.Fatal(h.item("I1"))
	}
	check("")
}

func TestIntakeDoesNotReportDirectAdoptionMissing(t *testing.T) {
	h := newHarness(t, nil)
	h.ok(h.eng.AdoptPR(ctx(), "github", "", adoption()))
	for _, result := range []flow.Result{h.intake(), h.intake(sourceItems(1)...), h.intake(sourceItems(1)...)} {
		if len(result.Notes) != 0 {
			t.Fatal(result)
		}
	}
	h.eng = h.reload()
	if result := h.intake(sourceItems(1)...); len(result.Notes) != 0 {
		t.Fatal(result)
	}
}

func TestIntakeIgnoresPRsAlreadyFollowedByTheItem(t *testing.T) {
	h := newHarness(t, nil)
	items := sourceItems(1)
	h.intake(items...)
	h.approvedItemAt("I1", 1, 1)
	claim := h.claim("example")
	h.toReview(claim.SliceID)
	items[0].PRs = []string{"acme/app#10"}
	for _, result := range []flow.Result{h.intake(items...), h.ok(h.eng.SyncBacklog(ctx(), "pat", "", "default", items))} {
		if len(result.Notes) != 0 || len(result.Events) != 0 {
			t.Fatal(result)
		}
	}
	items[0].PRs = append(items[0].PRs, "acme/app#12")
	wantNote(t, h.intake(items...), "the source names PRs acme/app#10, acme/app#12")
}

func TestIntakeSkipsRefsOfOtherWorkstreams(t *testing.T) {
	h := newHarness(t, nil)
	h.workstream("example", flow.PartialLimits{})
	h.backlogIn("example", "ACME-1")
	result := h.intake(sourceItems(1)...)
	wantEventTypes(t, result)
	wantNote(t, result, "ACME-1 is in workstream example; not synced")
	if h.item("I1").Workstream != "example" {
		t.Fatal("item moved")
	}
}

func TestBacklogSyncSkipsForeignClaimsEvenWithRepeatedRefs(t *testing.T) {
	h := newHarness(t, nil)
	items := sourceItems(1, 1, 2)
	items[1].ClaimedBy = "laptop-b"
	result := h.ok(h.eng.SyncBacklog(ctx(), "pat", "", "", items))
	wantEventTypes(t, result, flow.EvItemAdded)
	wantNote(t, result, "ACME-1 is claimed by laptop-b")
	if it := h.item("I1"); it.Ref != "ACME-2" || it.Rank != 3 {
		t.Fatal(it)
	}
}
