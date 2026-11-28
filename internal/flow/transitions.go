package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"time"
)

type Transition struct {
	Seq        int64          `json:"seq"`
	At         time.Time      `json:"at"`
	Workstream string         `json:"workstream"`
	ItemID     string         `json:"item_id"`
	Ref        string         `json:"ref"`
	SliceID    string         `json:"slice_id,omitempty"`
	Transition string         `json:"transition"`
	From       string         `json:"from,omitempty"`
	PR         *TransitionPR  `json:"pr,omitempty"`
	Actor      string         `json:"actor"`
	Runner     string         `json:"runner,omitempty"`
	Branch     string         `json:"branch,omitempty"`
	Ask        *TransitionAsk `json:"ask,omitempty"`
}

type TransitionPR struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Head   string `json:"head"`
}

type TransitionAsk struct {
	ID       string  `json:"id"`
	Type     AskType `json:"type"`
	Question string  `json:"question"`
	Owner    string  `json:"owner"`
	Answer   string  `json:"answer,omitempty"`
}

func (s *State) ApplyTransitions(e Event) ([]Transition, error) {
	var p struct {
		ItemID  string  `json:"item_id"`
		SliceID string  `json:"slice_id"`
		RunID   string  `json:"run_id"`
		FlagID  string  `json:"flag_id"`
		Subject Subject `json:"subject"`
	}
	if err := json.Unmarshal(e.Data, &p); err != nil {
		return nil, err
	}
	if p.SliceID == "" && s.Runs[p.RunID] != nil {
		p.SliceID = s.Runs[p.RunID].SliceID
	}
	if f := s.Flags[p.FlagID]; f != nil {
		p.Subject = f.Subject
	}
	if p.Subject.Kind == SubjectSlice {
		p.SliceID = p.Subject.ID
	}
	if p.Subject.Kind == SubjectItem {
		p.ItemID = p.Subject.ID
	}
	if p.ItemID == "" && s.Slices[p.SliceID] != nil {
		p.ItemID = s.Slices[p.SliceID].ItemID
	}
	it := s.Items[p.ItemID]
	var before ItemStatus
	old := map[string]Slice{}
	if it != nil {
		before = it.Status
		for _, id := range it.Slices {
			old[id] = *s.Slices[id]
		}
	}
	if err := s.Apply(e); err != nil {
		return nil, err
	}
	if e.Type == EvPRAdopted {
		it = s.Items[p.ItemID]
		base := Transition{Seq: e.Seq, At: e.At, Workstream: it.Workstream, ItemID: it.ID, Ref: it.Ref, Actor: e.Actor}
		sl := s.Slices[p.SliceID]
		adopted := base
		adopted.Transition, adopted.SliceID, adopted.Branch = "adopted", sl.ID, sl.Branch
		adopted.PR = &TransitionPR{Repo: sl.PR.Repo, Number: sl.PR.Number, Head: sl.PR.HeadSHA}
		base.From, base.Transition = string(before), string(ItemExecuting)
		return []Transition{adopted, base}, nil
	}
	if it == nil {
		return nil, nil
	}
	base := Transition{Seq: e.Seq, At: e.At, Workstream: it.Workstream, ItemID: it.ID, Ref: it.Ref, Actor: e.Actor}
	withSlice := func(t Transition, id string) Transition {
		t.SliceID = id
		if sl := s.Slices[id]; sl != nil && sl.PR != nil {
			t.PR = &TransitionPR{Repo: sl.PR.Repo, Number: sl.PR.Number, Head: sl.PR.HeadSHA}
		}
		return t
	}
	var out []Transition
	for _, id := range it.Slices {
		prior, exists := old[id]
		sl := s.Slices[id]
		if !exists || (prior.Status == sl.Status && prior.Rework == sl.Rework) {
			continue
		}
		t := withSlice(base, id)
		t.From = string(prior.Status)
		switch sl.Status {
		case SliceRunning:
			t.Transition = "claimed"
			if r := s.Runs[sl.RunID]; r != nil {
				t.Runner = r.RunnerID
			}
		case SliceSubmitted:
			t.Transition, t.Branch = "submitted", sl.Branch
		case SliceQueued:
			if sl.Rework && !prior.Rework {
				t.Transition = "rework"
			} else {
				t.Transition = "parked"
			}
		case SliceStalled:
			if sl.Rework && !prior.Rework {
				t.Transition = "rework"
			} else {
				t.Transition = "stalled"
			}
		default:
			t.Transition = string(sl.Status)
		}
		out = append(out, t)
	}
	if before != it.Status && it.Status != ItemShelved && before != ItemShelved {
		t := base
		t.From, t.Transition = string(before), string(it.Status)
		out = append(out, t)
	}
	if f := s.Flags[p.FlagID]; f != nil {
		t := withSlice(base, p.SliceID)
		switch e.Type {
		case EvFlagRaised:
			t.Transition = "asked"
		case EvFlagResolved:
			t.Transition = "answered"
		case EvFlagTimedOut:
			t.Transition = "ask_timed_out"
		}
		if t.Transition != "" {
			t.Ask = &TransitionAsk{ID: f.ID, Type: f.Ask, Question: f.Question, Owner: f.Owner, Answer: f.Answer}
			out = append(out, t)
		}
	}
	return out, nil
}

const EvPublisherAdvanced = "publisher_advanced"
const MaxPublisherState = 256 * 1024

type PublisherCursor struct {
	Seq   int64           `json:"seq"`
	State json.RawMessage `json:"state,omitempty"`
}

type publisherAdvanced struct {
	Workstream string `json:"workstream"`
	Publisher  string `json:"publisher"`
	PublisherCursor
}

func (e *Engine) Publisher(workstream, name string) (int64, json.RawMessage) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.state.Publishers[workstream+"/"+name]
	return p.Seq, bytes.Clone(p.State)
}

func (e *Engine) Transitions(workstream string, after int64, limit int) ([]Transition, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.transitionsAfter(workstream, after, limit)
}

func (e *Engine) transitionsAfter(workstream string, after int64, limit int) ([]Transition, bool) {
	out := []Transition{}
	start := sort.Search(len(e.transitions), func(index int) bool { return e.transitions[index].Seq > after })
	for _, t := range e.transitions[start:] {
		if t.Workstream != workstream {
			continue
		}
		if limit > 0 && len(out) >= limit && t.Seq != out[len(out)-1].Seq {
			return out, true
		}
		out = append(out, t)
	}
	return out, false
}

type PublishBatch struct {
	Workstream  string          `json:"workstream"`
	Publisher   string          `json:"publisher"`
	Cursor      int64           `json:"cursor"`
	State       json.RawMessage `json:"state,omitempty"`
	Transitions []Transition    `json:"transitions"`
	More        bool            `json:"more"`
}

func (e *Engine) PublisherBatch(workstream, name string, limit int) (PublishBatch, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.Workstreams[workstream] == nil {
		return PublishBatch{}, errUnknown("workstream", workstream)
	}
	if !workstreamName.MatchString(name) {
		return PublishBatch{}, fail(CodeInvalid, "invalid publisher name")
	}
	p := e.state.Publishers[workstream+"/"+name]
	transitions, more := e.transitionsAfter(workstream, p.Seq, limit)
	return PublishBatch{Workstream: workstream, Publisher: name, Cursor: p.Seq, State: bytes.Clone(p.State), Transitions: transitions, More: more}, nil
}

func (e *Engine) AdvancePublisher(ctx context.Context, actor, workstream, name string, after, seq int64, state json.RawMessage) (Result, error) {
	return e.execute(ctx, actor, "", func(d *decider) error {
		if d.s.Workstreams[workstream] == nil {
			return errUnknown("workstream", workstream)
		}
		if !workstreamName.MatchString(name) {
			return fail(CodeInvalid, "invalid publisher name")
		}
		p := d.s.Publishers[workstream+"/"+name]
		if p.Seq != after {
			return fail(CodeConflict, "publisher cursor changed")
		}
		if seq < after || seq > d.s.lastSeq {
			return fail(CodeInvalid, "invalid publisher sequence")
		}
		if state != nil {
			var compact bytes.Buffer
			if len(state) > MaxPublisherState || json.Compact(&compact, state) != nil {
				return fail(CodeInvalid, "invalid publisher state")
			}
			state = compact.Bytes()
		}
		if seq == after && (state == nil || bytes.Equal(state, p.State)) {
			return nil
		}
		d.emit(EvPublisherAdvanced, publisherAdvanced{Workstream: workstream, Publisher: name, PublisherCursor: PublisherCursor{Seq: seq, State: state}})
		return nil
	})
}
