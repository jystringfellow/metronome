package flow

import (
	"sort"
	"time"
)

type ItemStatus string

const (
	ItemReady           ItemStatus = "ready"
	ItemPlanning        ItemStatus = "planning"
	ItemExecuting       ItemStatus = "executing"
	ItemAwaitingRelease ItemStatus = "awaiting_release"
	ItemReleasing       ItemStatus = "releasing"
	ItemDone            ItemStatus = "done"
	ItemDropped         ItemStatus = "dropped"
)

type SliceStatus string

const (
	SliceQueued    SliceStatus = "queued"
	SliceRunning   SliceStatus = "running"
	SliceFlagged   SliceStatus = "flagged"
	SliceStalled   SliceStatus = "stalled"
	SliceSubmitted SliceStatus = "submitted"
	SlicePROpen    SliceStatus = "pr_open"
	SliceInReview  SliceStatus = "in_review"
	SliceApproved  SliceStatus = "approved"
	SliceMerged    SliceStatus = "merged"
)

type AskType string

const (
	AskDecide  AskType = "decide"
	AskAnswer  AskType = "answer"
	AskApprove AskType = "approve"
	AskVerify  AskType = "verify"
)

func (a AskType) Valid() bool {
	switch a {
	case AskDecide, AskAnswer, AskApprove, AskVerify:
		return true
	}
	return false
}

type Item struct {
	Workstream    string      `json:"workstream"`
	ID            string      `json:"id"`
	Ref           string      `json:"ref"`
	Title         string      `json:"title"`
	Intent        string      `json:"intent"`
	Owner         string      `json:"owner"`
	Value         float64     `json:"value"`
	PredictedTier int         `json:"predicted_tier"`
	ConfirmedTier int         `json:"confirmed_tier"`
	Rank          int         `json:"rank"`
	Status        ItemStatus  `json:"status"`
	HoldsPlan     bool        `json:"holds_plan"`
	HoldsRelease  bool        `json:"holds_release"`
	Front         bool        `json:"front"`
	Proposal      []SliceSpec `json:"proposal,omitempty"`
	Approver      string      `json:"approver,omitempty"`
	Slices        []string    `json:"slices"`
	AddedAt       time.Time   `json:"added_at"`
}

func (it *Item) needsRelease() bool {
	return it.ConfirmedTier >= 2
}

type SliceSpec struct {
	Title     string   `json:"title"`
	Repo      string   `json:"repo"`
	Criteria  []string `json:"criteria"`
	Approvers []string `json:"approvers"`
	After     []int    `json:"after,omitempty"`
}

type PR struct {
	Repo           string `json:"repo"`
	Number         int    `json:"number"`
	HeadSHA        string `json:"head_sha"`
	ApprovedSHA    string `json:"approved_sha,omitempty"`
	ChecksGreen    bool   `json:"checks_green"`
	ChecksRecorded bool   `json:"checks_recorded"`
}

type Slice struct {
	Workstream   string      `json:"workstream"`
	ID           string      `json:"id"`
	ItemID       string      `json:"item_id"`
	Seq          int         `json:"seq"`
	Title        string      `json:"title"`
	Repo         string      `json:"repo"`
	Criteria     []string    `json:"criteria"`
	Approvers    []string    `json:"approvers"`
	After        []string    `json:"after,omitempty"`
	WaitsOn      []string    `json:"waits_on,omitempty"`
	Status       SliceStatus `json:"status"`
	HoldsExecute bool        `json:"holds_execute"`
	HoldsReview  bool        `json:"holds_review"`
	Rework       bool        `json:"rework"`
	Front        bool        `json:"front"`
	RunID        string      `json:"run_id,omitempty"`
	StalledAt    time.Time   `json:"stalled_at,omitzero"`
	Branch       string      `json:"branch,omitempty"`
	Version      int         `json:"version"`
	PR           *PR         `json:"pr,omitempty"`
}

func (s *Slice) claimable() bool {
	return (s.Status == SliceQueued || s.Status == SliceStalled) && len(s.WaitsOn) == 0
}

type Dependency struct {
	SliceID string      `json:"slice_id"`
	Title   string      `json:"title"`
	Status  SliceStatus `json:"status"`
}

func (s *State) Dependencies(sliceID string) []Dependency {
	var deps []Dependency
	if sl := s.Slices[sliceID]; sl != nil {
		for _, id := range sl.After {
			dep := s.Slices[id]
			deps = append(deps, Dependency{SliceID: id, Title: dep.Title, Status: dep.Status})
		}
	}
	return deps
}

func without(ids []string, id string) []string {
	var remaining []string
	for _, other := range ids {
		if other != id {
			remaining = append(remaining, other)
		}
	}
	return remaining
}

type Run struct {
	ID           string    `json:"id"`
	SliceID      string    `json:"slice_id"`
	RunnerID     string    `json:"runner_id"`
	StartedAt    time.Time `json:"started_at"`
	LeaseUntil   time.Time `json:"lease_until"`
	Closed       bool      `json:"closed"`
	ClosedReason string    `json:"closed_reason,omitempty"`
	LastNote     string    `json:"last_note,omitempty"`
}

type Subject struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

const (
	SubjectItem  = "item"
	SubjectSlice = "slice"
)

type Flag struct {
	ID        string    `json:"id"`
	Subject   Subject   `json:"subject"`
	Ask       AskType   `json:"ask"`
	Question  string    `json:"question"`
	Owner     string    `json:"owner"`
	RaisedAt  time.Time `json:"raised_at"`
	TimeoutAt time.Time `json:"timeout_at"`
	Closed    bool      `json:"closed"`
	Outcome   string    `json:"outcome,omitempty"`
	Answer    string    `json:"answer,omitempty"`
}

type State struct {
	Workstreams map[string]*Workstream `json:"workstreams"`
	Items       map[string]*Item       `json:"items"`
	Slices      map[string]*Slice      `json:"slices"`
	Runs        map[string]*Run        `json:"runs"`
	Flags       map[string]*Flag       `json:"flags"`

	seen    map[string]struct{}
	lastSeq int64
	counter int
}

func NewState() *State {
	return &State{
		Workstreams: map[string]*Workstream{DefaultWorkstream: {Name: DefaultWorkstream, Limits: DefaultConfig().Limits.Stages()}},
		Items:       map[string]*Item{},
		Slices:      map[string]*Slice{},
		Runs:        map[string]*Run{},
		Flags:       map[string]*Flag{},
		seen:        map[string]struct{}{},
	}
}

type Usage struct {
	Ready       int            `json:"ready"`
	Plan        int            `json:"plan"`
	Execute     int            `json:"execute"`
	Review      int            `json:"review"`
	Release     int            `json:"release"`
	PerApprover map[string]int `json:"per_approver"`
}

func (s *State) Usage() Usage {
	return s.usage("")
}

func (s *State) usage(ws string) Usage {
	u := Usage{PerApprover: map[string]int{}}
	for _, it := range s.Items {
		if ws != "" && it.Workstream != ws {
			continue
		}
		if it.Status == ItemReady {
			u.Ready++
		}
		if it.HoldsPlan {
			u.Plan++
		}
		if it.HoldsRelease {
			u.Release++
		}
	}
	for _, sl := range s.Slices {
		if ws != "" && sl.Workstream != ws {
			continue
		}
		if sl.HoldsExecute {
			u.Execute++
		}
		if sl.HoldsReview {
			u.Review++
		}
		if sl.Status == SliceInReview {
			for _, a := range sl.Approvers {
				u.PerApprover[a]++
			}
		}
	}
	return u
}

func (s *State) itemSlices(itemID string) []*Slice {
	it := s.Items[itemID]
	if it == nil {
		return nil
	}
	out := make([]*Slice, 0, len(it.Slices))
	for _, id := range it.Slices {
		out = append(out, s.Slices[id])
	}
	return out
}

func (s *State) openFlagsFor(sub Subject) []*Flag {
	var out []*Flag
	for _, f := range s.Flags {
		if !f.Closed && f.Subject == sub {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *State) Inbox(owner string) []*Flag {
	var out []*Flag
	for _, f := range s.Flags {
		if !f.Closed && f.Owner == owner {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].RaisedAt.Equal(out[j].RaisedAt) {
			return out[i].RaisedAt.Before(out[j].RaisedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *State) readyItemsByPriority() []*Item {
	var out []*Item
	for _, it := range s.Items {
		if it.Status == ItemReady {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Workstream != out[j].Workstream {
			return out[i].Workstream < out[j].Workstream
		}
		if out[i].Front != out[j].Front {
			return out[i].Front
		}
		if out[i].Rank != out[j].Rank {
			return out[i].Rank < out[j].Rank
		}
		return out[i].ID < out[j].ID
	})
	positions := map[string]int{}
	indices := map[string]int{}
	for _, it := range out {
		indices[it.ID] = positions[it.Workstream]
		positions[it.Workstream]++
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i], out[j]
		if left.Front != right.Front {
			return left.Front
		}
		return crossLess(indices[left.ID], indices[right.ID], left, right, left.ID, right.ID)
	})
	return out
}

func (s *State) claimableByPriority() []*Slice {
	var out []*Slice
	for _, sl := range s.Slices {
		if sl.claimable() {
			out = append(out, sl)
		}
	}
	held := func(sl *Slice) bool { return sl.HoldsExecute || sl.HoldsReview }
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Workstream != b.Workstream {
			return a.Workstream < b.Workstream
		}
		if a.Rework != b.Rework {
			return a.Rework
		}
		if held(a) != held(b) {
			return held(a)
		}
		if a.Front != b.Front {
			return a.Front
		}
		ra, rb := s.Items[a.ItemID].Rank, s.Items[b.ItemID].Rank
		if ra != rb {
			return ra < rb
		}
		if a.ItemID != b.ItemID {
			return a.ItemID < b.ItemID
		}
		return a.Seq < b.Seq
	})
	positions := map[string]int{}
	indices := map[string]int{}
	for _, sl := range out {
		indices[sl.ID] = positions[sl.Workstream]
		positions[sl.Workstream]++
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i], out[j]
		if left.Rework != right.Rework {
			return left.Rework
		}
		if held(left) != held(right) {
			return held(left)
		}
		if left.Front != right.Front {
			return left.Front
		}
		return crossLess(indices[left.ID], indices[right.ID], s.Items[left.ItemID], s.Items[right.ItemID], left.ID, right.ID)
	})
	return out
}

func crossLess(leftPosition, rightPosition int, left, right *Item, leftID, rightID string) bool {
	if leftPosition != rightPosition {
		return leftPosition < rightPosition
	}
	if !left.AddedAt.Equal(right.AddedAt) {
		return left.AddedAt.Before(right.AddedAt)
	}
	if left.Workstream != right.Workstream {
		return left.Workstream < right.Workstream
	}
	return leftID < rightID
}

const DefaultWorkstream = "default"

type Workstream struct {
	Name      string      `json:"name"`
	Limits    StageLimits `json:"limits"`
	CreatedAt time.Time   `json:"created_at,omitzero"`
}

type StageUsage = StageLimits

func (s *State) StageUsage(ws string) StageUsage {
	u := s.usage(ws)
	return StageUsage{Ready: u.Ready, Plan: u.Plan, Execute: u.Execute, Review: u.Review, Release: u.Release}
}

type WorkstreamView struct {
	Name   string      `json:"name"`
	Limits StageLimits `json:"limits"`
	Usage  StageUsage  `json:"usage"`
}

func (s *State) WorkstreamViews() []WorkstreamView {
	out := make([]WorkstreamView, 0, len(s.Workstreams))
	for name, ws := range s.Workstreams {
		out = append(out, WorkstreamView{Name: name, Limits: ws.Limits, Usage: s.StageUsage(name)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
