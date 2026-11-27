package flow_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jystringfellow/metronome/internal/flow"
)

func TestBacklogLabels(t *testing.T) {
	h := newHarness(t, nil)
	items := sourceItems(1, 2)
	raw := []string{" Business ", "slow", "business", "Capability   Gap"}
	items[0].Labels = append([]string{}, raw...)
	result := h.ok(h.eng.SyncBacklog(ctx(), "pat", "", "", items))
	wantEventTypes(t, result, flow.EvItemAdded, flow.EvItemAdded)
	want := []string{"business", "capability gap", "slow"}
	if !reflect.DeepEqual(h.item("I1").Labels, want) || !reflect.DeepEqual(items[0].Labels, raw) || !reflect.DeepEqual(h.item("I2").Labels, []string{}) {
		t.Fatal("unexpected labels or mutated source", h.item("I1"), items)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(result.Events[0].Data, &payload); err != nil || string(payload["labels"]) != `["business","capability gap","slow"]` {
		t.Fatal(payload, err)
	}
	if strings.Contains(string(result.Events[1].Data), `"labels"`) {
		t.Fatal("unlabelled event changed", result.Events[1])
	}
	items[0].Labels = []string{"changed"}
	wantEventTypes(t, h.ok(h.eng.SyncBacklog(ctx(), "pat", "", "", items)))
	if !reflect.DeepEqual(h.item("I1").Labels, want) {
		t.Fatal("sync changed known item")
	}
}

func TestBacklogLabelLimits(t *testing.T) {
	var distinct []string
	for index := 1; index <= 21; index++ {
		distinct = append(distinct, fmt.Sprintf("label-%d", index))
	}
	for _, mode := range []string{"sync", "intake"} {
		t.Run(mode, func(t *testing.T) {
			for _, test := range []struct {
				name   string
				labels []string
			}{
				{"empty", []string{""}},
				{"whitespace", []string{" \t\n "}},
				{"length", []string{strings.Repeat("a", 41)}},
				{"count", distinct},
				{"comma", []string{"a,b"}},
				{"non-ASCII", []string{"café"}},
				{"leading punctuation", []string{"-slow"}},
			} {
				t.Run(test.name, func(t *testing.T) {
					h := newHarness(t, nil)
					items := sourceItems(1, 2)
					items[1].Labels = test.labels
					apply := h.eng.SyncBacklog
					if mode == "intake" {
						apply = h.eng.IntakeBacklog
					}
					result, err := apply(ctx(), "pat", "", "default", items)
					wantCode(t, err, flow.CodeInvalid)
					if len(result.Events) != 0 || len(h.eng.EventsAfter(0, 0)) != 0 || !strings.Contains(err.Error(), `"ACME-2"`) {
						t.Fatal(result, err)
					}
					if test.name != "count" && !strings.Contains(err.Error(), "label 1") {
						t.Fatal(err)
					}
				})
			}
			h := newHarness(t, nil)
			items := sourceItems(1)
			items[0].Labels = append([]string{strings.Repeat("a", 40)}, distinct[:19]...)
			items[0].Labels = append(items[0].Labels, " LABEL-1 ")
			apply := h.eng.SyncBacklog
			if mode == "intake" {
				apply = h.eng.IntakeBacklog
			}
			wantEventTypes(t, h.ok(apply(ctx(), "pat", "", "default", items)), flow.EvItemAdded)
			if len(h.item("I1").Labels) != 20 {
				t.Fatal(h.item("I1"))
			}
		})
	}
}

func TestIntakeUpdatesLabels(t *testing.T) {
	h := newHarness(t, nil)
	items := sourceItems(1)
	items[0].Labels = []string{"old"}
	h.intake(items...)
	items[0].Labels = []string{"Slow", "business"}
	result := h.intake(items...)
	wantEventTypes(t, result, flow.EvItemUpdated)
	if !strings.Contains(string(result.Events[0].Data), `"labels":["business","slow"]`) || !reflect.DeepEqual(h.item("I1").Labels, []string{"business", "slow"}) {
		t.Fatal(result, h.item("I1"))
	}
	items[0].Labels = []string{" BUSINESS ", "slow", "SLOW"}
	wantEventTypes(t, h.intake(items...))
	items[0].Labels = nil
	wantEventTypes(t, h.intake(items...), flow.EvItemUpdated)
	if !reflect.DeepEqual(h.item("I1").Labels, []string{}) {
		t.Fatal(h.item("I1"))
	}
	wantEventTypes(t, h.intake(), flow.EvItemShelved)
	items[0].Labels = []string{"report"}
	wantEventTypes(t, h.intake(items...), flow.EvItemRestored)
	if !reflect.DeepEqual(h.item("I1").Labels, []string{"report"}) {
		t.Fatal(h.item("I1"))
	}
	h.ok(h.eng.StartPlan(ctx(), "pat", "", "", "I1"))
	items[0].Labels = []string{"changed"}
	wantEventTypes(t, h.intake(items...))
	if !reflect.DeepEqual(h.item("I1").Labels, []string{"report"}) {
		t.Fatal(h.item("I1"))
	}
}

func TestLegacyLabelsReplay(t *testing.T) {
	h := newHarness(t, nil)
	_, err := h.store.Append(ctx(), []flow.Event{
		{ID: "added", Type: flow.EvItemAdded, At: h.clock.Now(), Actor: "pat", Data: json.RawMessage(`{"item_id":"I1","ref":"ACME-1","title":"first","owner":"pat","predicted_tier":1,"rank":1}`)},
		{ID: "updated", Type: flow.EvItemUpdated, At: h.clock.Now(), Actor: "pat", Data: json.RawMessage(`{"item_id":"I1","rank":2,"title":"second","intent":"updated"}`)},
		{ID: "shelved", Type: flow.EvItemShelved, At: h.clock.Now(), Actor: "pat", Data: json.RawMessage(`{"item_id":"I1","reason":"missing"}`)},
		{ID: "restored", Type: flow.EvItemRestored, At: h.clock.Now(), Actor: "pat", Data: json.RawMessage(`{"item_id":"I1","rank":3,"title":"third","intent":"restored"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.eng = h.reload()
	if it := h.item("I1"); it.Title != "third" || it.Intent != "restored" || it.Rank != 3 || it.Status != flow.ItemReady || !reflect.DeepEqual(it.Labels, []string{}) {
		t.Fatal(it)
	}
}
