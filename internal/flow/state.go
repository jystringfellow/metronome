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
	ID                 string      `json:"id"`
	ItemID             string      `json:"item_id"`
	Seq                int         `json:"seq"`
	Title              string      `json:"title"`
	Repo               string      `json:"repo"`
	Criteria           []string    `json:"criteria"`
	Approvers          []string    `json:"approvers"`
	Status             SliceStatus `json:"status"`
	HoldsExecute       bool        `json:"holds_execute"`
	HoldsReview        bool        `json:"holds_review"`
	Rework             bool        `json:"rework"`
	Resume             string      `json:"resume,omitempty"`
	ReworkReason       string      `json:"rework_reason,omitempty"`
	FailedHead         string      `json:"failed_head,omitempty"`
	ChangesRequestedBy string      `json:"changes_requested_by,omitempty"`
	LastRunID          string      `json:"last_run_id,omitempty"`
	Front              bool        `json:"front"`
	RunID              string      `json:"run_id,omitempty"`
	StalledAt          time.Time   `json:"stalled_at,omitzero"`
	Branch             string      `json:"branch,omitempty"`
	Version            int         `json:"version"`
	PR                 *PR         `json:"pr,omitempty"`
}

func (s *Slice) claimable() bool {
	return s.Status == SliceQueued || s.Status == SliceStalled
}

type Run struct {
	History      SliceHistory `json:"-"`
	ID           string       `json:"id"`
	SliceID      string       `json:"slice_id"`
	RunnerID     string       `json:"runner_id"`
	StartedAt    time.Time    `json:"started_at"`
	LeaseUntil   time.Time    `json:"lease_until"`
	Closed       bool         `json:"closed"`
	ClosedReason string       `json:"closed_reason,omitempty"`
	LastNote     string       `json:"last_note,omitempty"`
}

const (
	ResumeChecksFailed     = "checks_failed"
	ResumeChangesRequested = "changes_requested"
	ResumeLeaseExpired     = "lease_expired"
	ResumeDropped          = "dropped"
	ResumeAnswered         = "answered_ask"
	ResumeAskTimedOut      = "ask_timed_out"
)

type SliceHistory struct {
	Reason             string       `json:"reason"`
	Rework             bool         `json:"rework"`
	ReworkReason       string       `json:"rework_reason"`
	FailedHead         string       `json:"failed_head"`
	ChangesRequestedBy string       `json:"changes_requested_by"`
	PreviousRun        string       `json:"previous_run"`
	Branch             string       `json:"branch"`
	PR                 *HistoryPR   `json:"pr"`
	Asks               []HistoryAsk `json:"asks"`
}

type HistoryPR struct {
	Repo    string `json:"repo"`
	Number  int    `json:"number"`
	HeadSHA string `json:"head_sha"`
}

type HistoryAsk struct {
	ID         string    `json:"id"`
	Type       AskType   `json:"type"`
	Question   string    `json:"question"`
	Answer     string    `json:"answer"`
	AnsweredBy string    `json:"answered_by"`
	AnsweredAt time.Time `json:"answered_at"`
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
	ID         string    `json:"id"`
	Subject    Subject   `json:"subject"`
	Ask        AskType   `json:"ask"`
	Question   string    `json:"question"`
	Owner      string    `json:"owner"`
	RaisedAt   time.Time `json:"raised_at"`
	TimeoutAt  time.Time `json:"timeout_at"`
	Closed     bool      `json:"closed"`
	Outcome    string    `json:"outcome,omitempty"`
	Answer     string    `json:"answer,omitempty"`
	AnsweredBy string    `json:"answered_by,omitempty"`
	AnsweredAt time.Time `json:"answered_at,omitzero"`
}

type State struct {
	Items  map[string]*Item  `json:"items"`
	Slices map[string]*Slice `json:"slices"`
	Runs   map[string]*Run   `json:"runs"`
	Flags  map[string]*Flag  `json:"flags"`

	seen    map[string]struct{}
	lastSeq int64
	counter int
}

func NewState() *State {
	return &State{
		Items:  map[string]*Item{},
		Slices: map[string]*Slice{},
		Runs:   map[string]*Run{},
		Flags:  map[string]*Flag{},
		seen:   map[string]struct{}{},
	}
}

func (s *State) sliceHistory(sl *Slice) SliceHistory {
	history := SliceHistory{
		Reason: sl.Resume, Rework: sl.Rework, ReworkReason: sl.ReworkReason,
		FailedHead: sl.FailedHead, ChangesRequestedBy: sl.ChangesRequestedBy,
		PreviousRun: sl.LastRunID, Branch: sl.Branch, Asks: []HistoryAsk{},
	}
	if sl.PR != nil {
		history.PR = &HistoryPR{Repo: sl.PR.Repo, Number: sl.PR.Number, HeadSHA: sl.PR.HeadSHA}
	}
	var flags []*Flag
	for _, flag := range s.Flags {
		if flag.Subject.Kind == SubjectSlice && flag.Subject.ID == sl.ID && flag.Outcome == "resolved" {
			flags = append(flags, flag)
		}
	}
	sort.Slice(flags, func(i, j int) bool {
		left, right := flags[i], flags[j]
		if !left.RaisedAt.Equal(right.RaisedAt) {
			return left.RaisedAt.Before(right.RaisedAt)
		}
		if len(left.ID) != len(right.ID) {
			return len(left.ID) < len(right.ID)
		}
		return left.ID < right.ID
	})
	for _, flag := range flags {
		history.Asks = append(history.Asks, HistoryAsk{ID: flag.ID, Type: flag.Ask, Question: flag.Question,
			Answer: flag.Answer, AnsweredBy: flag.AnsweredBy, AnsweredAt: flag.AnsweredAt.UTC()})
	}
	return history
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
	u := Usage{PerApprover: map[string]int{}}
	for _, it := range s.Items {
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
		if out[i].Front != out[j].Front {
			return out[i].Front
		}
		if out[i].Rank != out[j].Rank {
			return out[i].Rank < out[j].Rank
		}
		return out[i].ID < out[j].ID
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
	return out
}
