package flow

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

type Event struct {
	Seq   int64           `json:"seq"`
	ID    string          `json:"id"`
	Type  string          `json:"type"`
	At    time.Time       `json:"at"`
	Actor string          `json:"actor"`
	Data  json.RawMessage `json:"data"`
}

const (
	EvWorkstreamCreated   = "workstream_created"
	EvWorkstreamLimitsSet = "workstream_limits_set"
	EvItemAdded           = "item_added"
	EvPlanStarted         = "plan_started"
	EvPlanProposed        = "plan_proposed"
	EvPlanApproved        = "plan_approved"
	EvPlanDropped         = "plan_dropped"
	EvItemParked          = "item_parked"
	EvFlagRaised          = "flag_raised"
	EvFlagResolved        = "flag_resolved"
	EvFlagTimedOut        = "flag_timed_out"
	EvSliceClaimed        = "slice_claimed"
	EvHeartbeat           = "heartbeat"
	EvRunClosed           = "run_closed"
	EvSliceParked         = "slice_parked"
	EvPROpened            = "pr_opened"
	EvChecksCompleted     = "checks_completed"
	EvPRReadyForReview    = "pr_ready_for_review"
	EvPRApproved          = "pr_approved"
	EvApprovalWithdrawn   = "approval_withdrawn"
	EvPRChangesRequested  = "pr_changes_requested"
	EvItemAwaitsRelease   = "item_awaits_release"
	EvReleaseSlotTaken    = "release_slot_taken"
	EvPRMerged            = "pr_merged"
	EvItemDone            = "item_done"
	EvReleaseDone         = "release_done"
)

const (
	CloseFlagged      = "flagged"
	CloseSubmitted    = "submitted"
	CloseReleased     = "released"
	CloseLeaseExpired = "lease_expired"
)

type itemAdded struct {
	Workstream    string      `json:"workstream,omitempty"`
	ItemID        string      `json:"item_id"`
	Ref           string      `json:"ref"`
	Title         string      `json:"title"`
	Intent        string      `json:"intent"`
	Owner         string      `json:"owner"`
	Value         float64     `json:"value"`
	PredictedTier int         `json:"predicted_tier"`
	Rank          int         `json:"rank"`
	Slices        []SliceSpec `json:"slices,omitempty"`
	TierDefaulted bool        `json:"tier_defaulted,omitempty"`
}

type itemRef struct {
	ItemID string `json:"item_id"`
}

type planProposed struct {
	ItemID   string      `json:"item_id"`
	Slices   []SliceSpec `json:"slices"`
	Approver string      `json:"approver"`
}

type planApproved struct {
	ItemID        string   `json:"item_id"`
	ConfirmedTier int      `json:"confirmed_tier"`
	SliceIDs      []string `json:"slice_ids"`
}

type flagRaised struct {
	FlagID    string    `json:"flag_id"`
	Subject   Subject   `json:"subject"`
	Ask       AskType   `json:"ask"`
	Question  string    `json:"question"`
	Owner     string    `json:"owner"`
	TimeoutAt time.Time `json:"timeout_at"`
}

type flagResolved struct {
	FlagID string `json:"flag_id"`
	Answer string `json:"answer"`
}

type flagRef struct {
	FlagID string `json:"flag_id"`
}

type sliceClaimed struct {
	Workstream   string    `json:"workstream,omitempty"`
	RunID        string    `json:"run_id"`
	SliceID      string    `json:"slice_id"`
	RunnerID     string    `json:"runner_id"`
	LeaseUntil   time.Time `json:"lease_until"`
	TakesExecute bool      `json:"takes_execute"`
	TakesReview  bool      `json:"takes_review"`
}

type heartbeat struct {
	RunID      string    `json:"run_id"`
	LeaseUntil time.Time `json:"lease_until"`
	Note       string    `json:"note"`
}

type runClosed struct {
	RunID  string `json:"run_id"`
	Reason string `json:"reason"`
	Branch string `json:"branch,omitempty"`
}

type sliceRef struct {
	SliceID string `json:"slice_id"`
}

type prOpened struct {
	SliceID string `json:"slice_id"`
	Repo    string `json:"repo"`
	Number  int    `json:"number"`
	HeadSHA string `json:"head_sha"`
}

type checksCompleted struct {
	SliceID string `json:"slice_id"`
	HeadSHA string `json:"head_sha"`
	Success bool   `json:"success"`
}

type prApproved struct {
	SliceID     string `json:"slice_id"`
	HeadSHA     string `json:"head_sha"`
	By          string `json:"by"`
	KeepsReview bool   `json:"keeps_review"`
}

type prChangesRequested struct {
	SliceID string `json:"slice_id"`
	By      string `json:"by"`
}

type prMerged struct {
	SliceID     string `json:"slice_id"`
	FreesReview bool   `json:"frees_review,omitempty"`
}

type approvalWithdrawn struct {
	SliceID      string `json:"slice_id"`
	WithdrawnSHA string `json:"withdrawn_sha"`
	HeadSHA      string `json:"head_sha"`
}

func (s *State) Apply(e Event) error {
	if err := s.apply(e); err != nil {
		return fmt.Errorf("apply %s %s: %w", e.Type, e.ID, err)
	}
	s.seen[e.ID] = struct{}{}
	if e.Seq > s.lastSeq {
		s.lastSeq = e.Seq
	}
	return nil
}

func (s *State) apply(e Event) error {
	switch e.Type {
	case EvWorkstreamCreated, EvWorkstreamLimitsSet:
		var p workstreamLimits
		if err := json.Unmarshal(e.Data, &p); err != nil {
			return err
		}
		if e.Type == EvWorkstreamCreated {
			if s.Workstreams[p.Workstream] != nil {
				return fail(CodeConflict, "workstream %s exists", p.Workstream)
			}
			s.Workstreams[p.Workstream] = &Workstream{Name: p.Workstream, Limits: p.Limits, CreatedAt: e.At}
		} else {
			ws := s.Workstreams[p.Workstream]
			if ws == nil {
				return errUnknown("workstream", p.Workstream)
			}
			ws.Limits = p.Limits
		}
	case EvItemAdded:
		var p itemAdded
		if err := json.Unmarshal(e.Data, &p); err != nil {
			return err
		}
		if p.Workstream == "" {
			p.Workstream = DefaultWorkstream
		}
		if s.Workstreams[p.Workstream] == nil {
			return errUnknown("workstream", p.Workstream)
		}
		s.Items[p.ItemID] = &Item{
			Workstream: p.Workstream,
			ID:         p.ItemID, Ref: p.Ref, Title: p.Title, Intent: p.Intent, Owner: p.Owner,
			Value: p.Value, PredictedTier: p.PredictedTier, Rank: p.Rank,
			PlannedSlices: p.Slices, TierDefaulted: p.TierDefaulted,
			Status: ItemReady, AddedAt: e.At, Slices: []string{},
		}
	case EvPlanStarted:
		it, err := s.itemFor(e.Data)
		if err != nil {
			return err
		}
		it.Status, it.HoldsPlan, it.Front = ItemPlanning, true, false
	case EvPlanProposed:
		var p planProposed
		if err := json.Unmarshal(e.Data, &p); err != nil {
			return err
		}
		it := s.Items[p.ItemID]
		if it == nil {
			return errUnknown("item", p.ItemID)
		}
		it.Proposal, it.Approver = p.Slices, p.Approver
	case EvPlanApproved:
		var p planApproved
		if err := json.Unmarshal(e.Data, &p); err != nil {
			return err
		}
		it := s.Items[p.ItemID]
		if it == nil {
			return errUnknown("item", p.ItemID)
		}
		it.Status, it.HoldsPlan, it.PredictedTier = ItemExecuting, false, p.ConfirmedTier
		it.ConfirmedTier = p.ConfirmedTier
		for i, spec := range it.Proposal {
			if i >= len(p.SliceIDs) {
				return fmt.Errorf("plan approval for %s is missing slice %d", it.ID, i+1)
			}
			id := p.SliceIDs[i]
			var after []string
			for _, position := range spec.After {
				if position < 1 || position > len(p.SliceIDs) || position > len(it.Proposal) {
					return fmt.Errorf("slice %s lists invalid after position %d", id, position)
				}
				after = append(after, p.SliceIDs[position-1])
			}
			s.Slices[id] = &Slice{
				Workstream: it.Workstream,
				ID:         id, ItemID: it.ID, Seq: i + 1, Title: spec.Title, Repo: spec.Repo,
				Criteria: spec.Criteria, Approvers: spec.Approvers, Status: SliceQueued,
				After: after, WaitsOn: slices.Clone(after),
			}
			it.Slices = append(it.Slices, id)
		}
		it.Proposal = nil
	case EvPlanDropped:
		it, err := s.itemFor(e.Data)
		if err != nil {
			return err
		}
		it.Status, it.HoldsPlan, it.Proposal = ItemDropped, false, nil
	case EvItemParked:
		it, err := s.itemFor(e.Data)
		if err != nil {
			return err
		}
		it.Status, it.HoldsPlan, it.Front, it.Proposal = ItemReady, false, true, nil
	case EvFlagRaised:
		var p flagRaised
		if err := json.Unmarshal(e.Data, &p); err != nil {
			return err
		}
		s.Flags[p.FlagID] = &Flag{
			ID: p.FlagID, Subject: p.Subject, Ask: p.Ask, Question: p.Question,
			Owner: p.Owner, RaisedAt: e.At, TimeoutAt: p.TimeoutAt,
		}
	case EvFlagResolved:
		var p flagResolved
		if err := json.Unmarshal(e.Data, &p); err != nil {
			return err
		}
		f := s.Flags[p.FlagID]
		if f == nil {
			return errUnknown("flag", p.FlagID)
		}
		f.Closed, f.Outcome, f.Answer = true, "resolved", p.Answer
		if f.Subject.Kind == SubjectSlice {
			if sl := s.Slices[f.Subject.ID]; sl != nil && sl.Status == SliceFlagged && len(s.openFlagsFor(f.Subject)) == 0 {
				sl.Status, sl.StalledAt = SliceStalled, e.At
			}
		}
	case EvFlagTimedOut:
		var p flagRef
		if err := json.Unmarshal(e.Data, &p); err != nil {
			return err
		}
		f := s.Flags[p.FlagID]
		if f == nil {
			return errUnknown("flag", p.FlagID)
		}
		f.Closed, f.Outcome = true, "timed_out"
	case EvSliceClaimed:
		var p sliceClaimed
		if err := json.Unmarshal(e.Data, &p); err != nil {
			return err
		}
		sl := s.Slices[p.SliceID]
		if sl == nil {
			return errUnknown("slice", p.SliceID)
		}
		s.Runs[p.RunID] = &Run{ID: p.RunID, SliceID: p.SliceID, RunnerID: p.RunnerID, StartedAt: e.At, LeaseUntil: p.LeaseUntil}
		sl.Status, sl.RunID, sl.Front, sl.StalledAt = SliceRunning, p.RunID, false, time.Time{}
		sl.Version++
		sl.HoldsExecute = sl.HoldsExecute || p.TakesExecute
		sl.HoldsReview = sl.HoldsReview || p.TakesReview
	case EvHeartbeat:
		var p heartbeat
		if err := json.Unmarshal(e.Data, &p); err != nil {
			return err
		}
		r := s.Runs[p.RunID]
		if r == nil {
			return errUnknown("run", p.RunID)
		}
		r.LeaseUntil, r.LastNote = p.LeaseUntil, p.Note
	case EvRunClosed:
		var p runClosed
		if err := json.Unmarshal(e.Data, &p); err != nil {
			return err
		}
		r := s.Runs[p.RunID]
		if r == nil {
			return errUnknown("run", p.RunID)
		}
		r.Closed, r.ClosedReason = true, p.Reason
		sl := s.Slices[r.SliceID]
		sl.RunID = ""
		sl.Version++
		switch p.Reason {
		case CloseFlagged:
			sl.Status = SliceFlagged
		case CloseSubmitted:
			sl.Status, sl.Branch, sl.Rework = SliceSubmitted, p.Branch, false
		case CloseReleased:
			sl.Status, sl.StalledAt = SliceStalled, e.At
		case CloseLeaseExpired:
			sl.Status, sl.StalledAt = SliceStalled, r.LeaseUntil
		default:
			return fmt.Errorf("unknown close reason %q", p.Reason)
		}
	case EvSliceParked:
		sl, err := s.sliceFor(e.Data)
		if err != nil {
			return err
		}
		sl.Status, sl.HoldsExecute, sl.HoldsReview, sl.Front, sl.StalledAt = SliceQueued, false, false, true, time.Time{}
		sl.Version++
	case EvPROpened:
		var p prOpened
		if err := json.Unmarshal(e.Data, &p); err != nil {
			return err
		}
		sl := s.Slices[p.SliceID]
		if sl == nil {
			return errUnknown("slice", p.SliceID)
		}
		sl.PR = &PR{Repo: p.Repo, Number: p.Number, HeadSHA: p.HeadSHA}
		sl.Version++
		if sl.Status == SliceSubmitted {
			sl.Status = SlicePROpen
		}
	case EvChecksCompleted:
		var p checksCompleted
		if err := json.Unmarshal(e.Data, &p); err != nil {
			return err
		}
		sl := s.Slices[p.SliceID]
		if sl == nil || sl.PR == nil {
			return errUnknown("slice PR", p.SliceID)
		}
		sl.PR.HeadSHA, sl.PR.ChecksGreen, sl.PR.ChecksRecorded = p.HeadSHA, p.Success, true
		sl.Version++
		if !p.Success && (sl.Status == SlicePROpen || sl.Status == SliceSubmitted || sl.Status == SliceInReview) {
			sl.Status, sl.Rework, sl.StalledAt = SliceStalled, true, e.At
		}
	case EvPRReadyForReview:
		sl, err := s.sliceFor(e.Data)
		if err != nil {
			return err
		}
		sl.Status, sl.HoldsExecute = SliceInReview, false
		sl.Version++
	case EvPRApproved:
		var p prApproved
		if err := json.Unmarshal(e.Data, &p); err != nil {
			return err
		}
		sl := s.Slices[p.SliceID]
		if sl == nil || sl.PR == nil {
			return errUnknown("slice PR", p.SliceID)
		}
		sl.Status, sl.PR.ApprovedSHA, sl.HoldsReview = SliceApproved, p.HeadSHA, p.KeepsReview
		sl.Version++
	case EvApprovalWithdrawn:
		var p approvalWithdrawn
		if err := json.Unmarshal(e.Data, &p); err != nil {
			return err
		}
		sl := s.Slices[p.SliceID]
		if sl == nil || sl.PR == nil {
			return errUnknown("slice PR", p.SliceID)
		}
		sl.Status, sl.HoldsReview = SlicePROpen, true
		sl.Version++
		it := s.Items[sl.ItemID]
		if it.Status == ItemReleasing || it.Status == ItemAwaitingRelease {
			it.Status, it.HoldsRelease = ItemExecuting, false
		}
		for _, sib := range s.itemSlices(it.ID) {
			if sib.Status == SliceApproved || sib.Status == SliceMerged {
				sib.HoldsReview = false
			}
		}
	case EvPRChangesRequested:
		var p prChangesRequested
		if err := json.Unmarshal(e.Data, &p); err != nil {
			return err
		}
		sl := s.Slices[p.SliceID]
		if sl == nil {
			return errUnknown("slice", p.SliceID)
		}
		sl.Status, sl.Rework = SliceQueued, true
		sl.Version++
	case EvItemAwaitsRelease:
		it, err := s.itemFor(e.Data)
		if err != nil {
			return err
		}
		it.Status = ItemAwaitingRelease
	case EvReleaseSlotTaken:
		it, err := s.itemFor(e.Data)
		if err != nil {
			return err
		}
		it.Status, it.HoldsRelease = ItemReleasing, true
		for _, sl := range s.itemSlices(it.ID) {
			sl.HoldsReview = false
		}
	case EvPRMerged:
		var p prMerged
		if err := json.Unmarshal(e.Data, &p); err != nil {
			return err
		}
		sl := s.Slices[p.SliceID]
		if sl == nil {
			return errUnknown("slice", p.SliceID)
		}
		sl.Status = SliceMerged
		for _, sibling := range s.itemSlices(sl.ItemID) {
			if slices.Contains(sibling.WaitsOn, sl.ID) {
				sibling.WaitsOn = without(sibling.WaitsOn, sl.ID)
			}
		}
		if p.FreesReview {
			sl.HoldsReview = false
		}
		sl.Version++
	case EvItemDone:
		it, err := s.itemFor(e.Data)
		if err != nil {
			return err
		}
		it.Status, it.HoldsRelease = ItemDone, false
		for _, sl := range s.itemSlices(it.ID) {
			sl.HoldsReview, sl.HoldsExecute = false, false
		}
	case EvReleaseDone:
		it, err := s.itemFor(e.Data)
		if err != nil {
			return err
		}
		it.Status, it.HoldsRelease = ItemDone, false
	default:
		return fmt.Errorf("unknown event type %q", e.Type)
	}
	return nil
}

func (s *State) itemFor(data json.RawMessage) (*Item, error) {
	var p itemRef
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	it := s.Items[p.ItemID]
	if it == nil {
		return nil, errUnknown("item", p.ItemID)
	}
	return it, nil
}

func (s *State) sliceFor(data json.RawMessage) (*Slice, error) {
	var p sliceRef
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	sl := s.Slices[p.SliceID]
	if sl == nil {
		return nil, errUnknown("slice", p.SliceID)
	}
	return sl, nil
}
