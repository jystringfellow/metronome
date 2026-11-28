package flow_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/jystringfellow/metronome/internal/flow"
)

func TestTransitionLifecycleAndReplay(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItemAt("I1", 1, 1)
	claim := h.claim("laptop-a")
	h.toReview(claim.SliceID)
	h.ok(h.eng.PRApproved(ctx(), "github", "", claim.SliceID, "sha-"+claim.SliceID, "rev"))
	h.ok(h.eng.PRMerged(ctx(), "github", "", claim.SliceID, "rev"))
	transitions, more := h.eng.Transitions("default", 0, 100)
	want := []string{"planning", "asked", "answered", "executing", "claimed", "submitted", "pr_open", "in_review", "approved", "merged", "done"}
	if more || len(transitions) != len(want) {
		t.Fatal(transitions, more)
	}
	for index, transition := range transitions {
		if transition.Transition != want[index] || transition.ItemID != "I1" || transition.Ref != "ACME-1" || transition.Actor == "" || transition.At.IsZero() {
			t.Fatal(transition)
		}
		if index >= 4 && index <= 9 && transition.SliceID != claim.SliceID {
			t.Fatal(transition)
		}
		if index >= 6 && index <= 9 && (transition.PR == nil || transition.PR.Repo != "acme/app" || transition.PR.Head != "sha-"+claim.SliceID) {
			t.Fatal(transition)
		}
		if index > 0 && transition.Seq <= transitions[index-1].Seq {
			t.Fatal(transition)
		}
	}
	if transitions[4].Runner != "laptop-a" || transitions[5].Branch != "metronome/"+claim.SliceID || transitions[1].Ask == nil || transitions[2].Ask == nil {
		t.Fatal(transitions)
	}
	replayed, _ := h.reload().Transitions("default", 0, 100)
	if !reflect.DeepEqual(transitions, replayed) {
		t.Fatal(replayed)
	}
	plain, derived := flow.NewState(), flow.NewState()
	for _, event := range h.eng.EventsAfter(0, 0) {
		if err := plain.Apply(event); err != nil {
			t.Fatal(err)
		}
		if _, err := derived.ApplyTransitions(event); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := json.Marshal(plain)
	second, _ := json.Marshal(derived)
	if string(first) != string(second) {
		t.Fatal(string(first), string(second))
	}
}

func TestReworkFixedTransition(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItem("I1", 1)
	claim := h.claim("laptop")
	h.toReview(claim.SliceID)
	h.ok(h.eng.ChecksCompleted(ctx(), "github", "", claim.SliceID, "sha-"+claim.SliceID, false))
	events := h.eng.EventsAfter(0, 0)
	h.ok(h.eng.ReworkFixed(ctx(), "github", "", claim.SliceID, "acme/app", 10, "ccc", "sam"))
	transitions, more := h.eng.Transitions("default", events[len(events)-1].Seq, 100)
	if more || len(transitions) != 1 || transitions[0].Transition != "in_review" || transitions[0].From != "stalled" || transitions[0].PR == nil || transitions[0].PR.Head != "ccc" {
		t.Fatal(transitions, more)
	}
}

func TestTransitionWorkstreamBatchAndWithdrawal(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.ok(h.eng.CreateWorkstream(ctx(), "pat", "", "example", flow.PartialLimits{}))
	h.ok(h.eng.SyncBacklog(ctx(), "pat", "", "example", []flow.BacklogItem{{Ref: "ACME-2", Title: "other", Owner: "pat"}}))
	h.ok(h.eng.StartPlan(ctx(), "pat", "", "example", "I2"))
	h.approvedItemAt("I1", 1, 2)
	id := h.claimAndApprove()
	h.newHead(id, "new-head")
	other, more := h.eng.Transitions("example", 0, 1)
	if len(other) != 1 || other[0].ItemID != "I2" || more {
		t.Fatal(other, more)
	}
	all, _ := h.eng.Transitions("default", 0, 100)
	for _, transition := range all {
		if transition.ItemID != "I1" {
			t.Fatal(transition)
		}
	}
	last := all[len(all)-1]
	batch, more := h.eng.Transitions("default", last.Seq-1, 1)
	if more || len(batch) != 2 || batch[0].Transition != "pr_open" || batch[1].Transition != "executing" || batch[0].Seq != batch[1].Seq {
		t.Fatal(batch, more)
	}
	first, more := h.eng.Transitions("default", 0, 1)
	if !more || len(first) != 1 || first[0].Transition != "planning" {
		t.Fatal(first, more)
	}
}

func TestPublisherCursorReplayAndValidation(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItemAt("I1", 1, 1)
	batch, err := h.eng.PublisherBatch("default", "board", 1)
	if err != nil || len(batch.Transitions) != 1 || !batch.More || batch.Cursor != 0 {
		t.Fatal(batch, err)
	}
	seq := batch.Transitions[0].Seq
	h.ok(h.eng.AdvancePublisher(ctx(), "pat", "default", "board", 0, 0, json.RawMessage(`{"status":"Ready"}`)))
	h.ok(h.eng.AdvancePublisher(ctx(), "pat", "default", "board", 0, seq, nil))
	_, err = h.eng.AdvancePublisher(ctx(), "pat", "default", "board", 0, seq, nil)
	wantCode(t, err, flow.CodeConflict)
	for _, invalid := range []struct {
		ws, name   string
		after, seq int64
		state      json.RawMessage
		code       flow.ErrorCode
	}{
		{"missing", "board", 0, 0, nil, flow.CodeNotFound},
		{"default", "bad/name", 0, 0, nil, flow.CodeInvalid},
		{"default", "board", seq, seq - 1, nil, flow.CodeInvalid},
		{"default", "board", seq, 9999, nil, flow.CodeInvalid},
		{"default", "board", seq, seq, json.RawMessage(`{`), flow.CodeInvalid},
	} {
		_, err := h.eng.AdvancePublisher(ctx(), "pat", invalid.ws, invalid.name, invalid.after, invalid.seq, invalid.state)
		wantCode(t, err, invalid.code)
	}
	_, err = h.eng.AdvancePublisher(ctx(), "pat", "default", "board", seq, seq, json.RawMessage(`"`+strings.Repeat("x", flow.MaxPublisherState)+`"`))
	wantCode(t, err, flow.CodeInvalid)
	if result := h.ok(h.eng.AdvancePublisher(ctx(), "pat", "default", "board", seq, seq, nil)); len(result.Events) != 0 {
		t.Fatal(result)
	}
	restarted := h.reload()
	cursor, state := restarted.Publisher("default", "board")
	if cursor != seq || string(state) != `{"status":"Ready"}` {
		t.Fatal(cursor, string(state))
	}
	if cursor, _ := restarted.Publisher("default", "other"); cursor != 0 {
		t.Fatal(cursor)
	}
	batch, err = restarted.PublisherBatch("default", "board", 100)
	if err != nil || len(batch.Transitions) != 3 || batch.Transitions[2].Transition != "executing" {
		t.Fatal(batch, err)
	}
}

func TestTransitionWaitingAndRework(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItemAt("I1", 1, 1)
	claim := h.claim("laptop-a")
	h.ok(h.eng.FlagRun(ctx(), "laptop-a", "", claim.RunID, flow.AskAnswer, "CSV or JSON?", "pat"))
	var flagID string
	h.eng.View(func(state *flow.State) { flagID = state.Inbox("pat")[0].ID })
	h.ok(h.eng.ResolveFlag(ctx(), "pat", "", flagID, "CSV"))
	claim = h.claim("laptop-a")
	h.clock.advance(h.cfg.Lease)
	h.ok(h.eng.Tick(ctx()))
	h.clock.advance(h.cfg.StallTimeout)
	h.ok(h.eng.Tick(ctx()))
	claim = h.claim("laptop-a")
	h.ok(h.eng.Submit(ctx(), "laptop-a", "", claim.RunID, "example"))
	h.ok(h.eng.PROpened(ctx(), "github", "", claim.SliceID, "acme/app", 7, "abc"))
	h.ok(h.eng.ChecksCompleted(ctx(), "github", "", claim.SliceID, "abc", false))
	transitions, _ := h.eng.Transitions("default", 0, 100)
	var names []string
	for _, transition := range transitions {
		names = append(names, transition.Transition)
	}
	want := []string{"planning", "asked", "answered", "executing", "claimed", "flagged", "asked", "stalled", "answered", "claimed", "stalled", "parked", "claimed", "submitted", "pr_open", "rework"}
	if !reflect.DeepEqual(names, want) {
		t.Fatal(names)
	}
	if transitions[6].Ask == nil || transitions[8].Ask == nil || transitions[8].Ask.Answer != "CSV" || transitions[6].SliceID != claim.SliceID || transitions[7].Seq != transitions[8].Seq {
		t.Fatal(transitions)
	}
}

func TestTransitionIntakeDoesNotEchoSource(t *testing.T) {
	h := newHarness(t, nil)
	items := []flow.BacklogItem{{Ref: "ACME-1", Title: "Example", Owner: "pat"}}
	h.ok(h.eng.IntakeBacklog(ctx(), "pat", "", "default", items))
	h.ok(h.eng.IntakeBacklog(ctx(), "pat", "", "default", nil))
	h.ok(h.eng.IntakeBacklog(ctx(), "pat", "", "default", items))
	if transitions, _ := h.eng.Transitions("default", 0, 100); len(transitions) != 0 {
		t.Fatal(transitions)
	}
	h.ok(h.eng.StartPlan(ctx(), "pat", "", "default", "I1"))
	h.ok(h.eng.DropPlan(ctx(), "pat", "", "I1"))
	if transitions, _ := h.eng.Transitions("default", 0, 100); len(transitions) != 2 || transitions[1].Transition != "dropped" {
		t.Fatal(transitions)
	}
}

func TestTransitionChangesRequestedAndReworkParked(t *testing.T) {
	h := newHarness(t, nil)
	h.backlog(1)
	h.approvedItemAt("I1", 1, 1)
	claim := h.claim("laptop-a")
	h.toReview(claim.SliceID)
	h.ok(h.eng.PRChangesRequested(ctx(), "github", "", claim.SliceID, "rev"))
	h.claim("laptop-a")
	h.clock.advance(h.cfg.Lease)
	h.ok(h.eng.Tick(ctx()))
	h.clock.advance(h.cfg.StallTimeout)
	h.ok(h.eng.Tick(ctx()))
	transitions, _ := h.eng.Transitions("default", 0, 100)
	var names []string
	for _, transition := range transitions {
		names = append(names, transition.Transition)
	}
	want := []string{"planning", "asked", "answered", "executing", "claimed", "submitted", "pr_open", "in_review", "rework", "claimed", "stalled", "parked"}
	if !reflect.DeepEqual(names, want) {
		t.Fatal(names)
	}
	if slice := h.slice(claim.SliceID); !slice.Rework || slice.Status != flow.SliceQueued {
		t.Fatal(slice)
	}
	if parked := transitions[len(transitions)-1]; parked.From != "stalled" || parked.SliceID != claim.SliceID || parked.PR == nil {
		t.Fatal(parked)
	}
}

func TestTransitionTierTwoRelease(t *testing.T) {
	h := newHarness(t, func(config *flow.Config) { config.Limits.Release = 1 })
	h.backlog(2)
	h.approvedItemAt("I1", 1, 2)
	h.approvedItemAt("I2", 1, 2)
	first, second := h.claimAndApprove(), h.claimAndApprove()
	h.ok(h.eng.PRMerged(ctx(), "github", "", first, "rev"))
	h.ok(h.eng.ReleaseDone(ctx(), "pat", "", h.slice(first).ItemID))
	h.ok(h.eng.PRMerged(ctx(), "github", "", second, "rev"))
	h.ok(h.eng.ReleaseDone(ctx(), "pat", "", h.slice(second).ItemID))
	transitions, _ := h.eng.Transitions("default", 0, 100)
	stages := map[string][]string{}
	for _, transition := range transitions {
		switch transition.Transition {
		case "awaiting_release", "releasing", "merged", "done":
			stages[transition.ItemID] = append(stages[transition.ItemID], transition.Transition)
		}
	}
	want := map[string][]string{
		"I1": {"releasing", "merged", "done"},
		"I2": {"awaiting_release", "releasing", "merged", "done"},
	}
	if !reflect.DeepEqual(stages, want) {
		t.Fatal(stages)
	}
}
