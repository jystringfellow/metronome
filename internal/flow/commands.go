package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

type BacklogItem struct {
	Ref           string  `json:"ref"`
	Title         string  `json:"title"`
	Intent        string  `json:"intent"`
	Owner         string  `json:"owner"`
	Value         float64 `json:"value"`
	PredictedTier int     `json:"predicted_tier"`
}

func (e *Engine) SyncBacklog(ctx context.Context, actor, requestID string, items []BacklogItem) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		known := map[string]bool{}
		for _, it := range d.s.Items {
			known[it.Ref] = true
		}
		for i, bi := range items {
			if bi.Ref == "" || bi.Title == "" || bi.Owner == "" {
				return fail(CodeInvalid, "backlog item %d needs ref, title and owner", i+1)
			}
			if bi.PredictedTier < 0 || bi.PredictedTier > 3 {
				return fail(CodeInvalid, "backlog item %q has tier %d, want 0 to 3", bi.Ref, bi.PredictedTier)
			}
		}
		ready := d.s.Usage().Ready
		if ready >= d.cfg.Limits.ReadyRefillBelow {
			return nil
		}
		for i, bi := range items {
			if ready >= d.cfg.Limits.Ready {
				break
			}
			if known[bi.Ref] {
				continue
			}
			known[bi.Ref] = true
			d.emit(EvItemAdded, itemAdded{
				ItemID: d.nextItemID(), Ref: bi.Ref, Title: bi.Title, Intent: bi.Intent, Owner: bi.Owner,
				Value: bi.Value, PredictedTier: bi.PredictedTier, Rank: i + 1,
			})
			ready++
		}
		return nil
	})
}

func (e *Engine) StartPlan(ctx context.Context, actor, requestID, itemID string) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		if d.s.Usage().Plan >= d.cfg.Limits.Plan {
			return fail(CodeNoCapacity, "Plan is at its limit of %d", d.cfg.Limits.Plan)
		}
		var it *Item
		if itemID == "" {
			ready := d.s.readyItemsByPriority()
			if len(ready) == 0 {
				return fail(CodeNoWork, "no ready items")
			}
			it = ready[0]
		} else {
			it = d.s.Items[itemID]
			if it == nil {
				return errUnknown("item", itemID)
			}
			if it.Status != ItemReady {
				return fail(CodeConflict, "item %s is %s, not ready", it.ID, it.Status)
			}
		}
		d.emit(EvPlanStarted, itemRef{ItemID: it.ID})
		return nil
	})
}

func (e *Engine) ProposePlan(ctx context.Context, actor, requestID, itemID string, slices []SliceSpec, approver string) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		it, err := d.planningItem(itemID)
		if err != nil {
			return err
		}
		if len(slices) == 0 {
			return fail(CodeInvalid, "a plan needs at least one slice")
		}
		for i, sl := range slices {
			if sl.Title == "" || sl.Repo == "" {
				return fail(CodeInvalid, "slice %d needs a title and a repo", i+1)
			}
		}
		if approver == "" {
			approver = it.Owner
		}
		d.closeOpenFlags(Subject{Kind: SubjectItem, ID: it.ID}, AskApprove, "superseded by a new proposal")
		d.emit(EvPlanProposed, planProposed{ItemID: it.ID, Slices: slices, Approver: approver})
		d.raiseFlag(Subject{Kind: SubjectItem, ID: it.ID}, AskApprove, approver,
			fmt.Sprintf("Approve the plan for %s (%d slices) and confirm tier %d?", it.Title, len(slices), it.PredictedTier))
		return nil
	})
}

func (e *Engine) ApprovePlan(ctx context.Context, actor, requestID, itemID string, confirmedTier *int) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		it, err := d.planningItem(itemID)
		if err != nil {
			return err
		}
		if len(it.Proposal) == 0 {
			return fail(CodeConflict, "item %s has no proposed plan", it.ID)
		}
		tier := it.PredictedTier
		if confirmedTier != nil {
			if *confirmedTier < 0 || *confirmedTier > 3 {
				return fail(CodeInvalid, "tier %d, want 0 to 3", *confirmedTier)
			}
			tier = *confirmedTier
		}
		ids := make([]string, len(it.Proposal))
		for i := range it.Proposal {
			ids[i] = fmt.Sprintf("%s.%d", it.ID, i+1)
		}
		d.closeOpenFlags(Subject{Kind: SubjectItem, ID: it.ID}, "", "plan approved")
		d.emit(EvPlanApproved, planApproved{ItemID: it.ID, ConfirmedTier: tier, SliceIDs: ids})
		return nil
	})
}

func (e *Engine) DropPlan(ctx context.Context, actor, requestID, itemID string) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		it, err := d.planningItem(itemID)
		if err != nil {
			return err
		}
		d.closeOpenFlags(Subject{Kind: SubjectItem, ID: it.ID}, "", "plan dropped")
		d.emit(EvPlanDropped, itemRef{ItemID: it.ID})
		return nil
	})
}

func (e *Engine) RaiseFlag(ctx context.Context, actor, requestID string, subject Subject, ask AskType, question, owner string) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		if !ask.Valid() {
			return fail(CodeInvalid, "unknown ask type %q", ask)
		}
		if question == "" {
			return fail(CodeInvalid, "a flag needs a question")
		}
		var defaultOwner string
		switch subject.Kind {
		case SubjectItem:
			it := d.s.Items[subject.ID]
			if it == nil {
				return errUnknown("item", subject.ID)
			}
			defaultOwner = it.Owner
		case SubjectSlice:
			sl := d.s.Slices[subject.ID]
			if sl == nil {
				return errUnknown("slice", subject.ID)
			}
			if sl.Status == SliceRunning {
				return fail(CodeConflict, "slice %s has a run; flag it through run %s", sl.ID, sl.RunID)
			}
			defaultOwner = d.s.Items[sl.ItemID].Owner
		default:
			return fail(CodeInvalid, "unknown subject kind %q", subject.Kind)
		}
		if owner == "" {
			owner = defaultOwner
		}
		d.raiseFlag(subject, ask, owner, question)
		return nil
	})
}

func (e *Engine) ResolveFlag(ctx context.Context, actor, requestID, flagID, answer string) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		f := d.s.Flags[flagID]
		if f == nil {
			return errUnknown("flag", flagID)
		}
		if f.Closed {
			return fail(CodeConflict, "flag %s is already closed (%s)", f.ID, f.Outcome)
		}
		if f.Ask == AskApprove && f.Subject.Kind == SubjectItem {
			return fail(CodeConflict, "flag %s asks for plan approval; approve or drop the plan instead", f.ID)
		}
		d.emit(EvFlagResolved, flagResolved{FlagID: f.ID, Answer: answer})
		return nil
	})
}

type Claim struct {
	RunID      string    `json:"run_id"`
	SliceID    string    `json:"slice_id"`
	LeaseUntil time.Time `json:"lease_until"`
}

func ClaimFrom(r Result) (Claim, bool) {
	for _, ev := range r.Events {
		if ev.Type == EvSliceClaimed {
			var p sliceClaimed
			if err := json.Unmarshal(ev.Data, &p); err == nil {
				return Claim{RunID: p.RunID, SliceID: p.SliceID, LeaseUntil: p.LeaseUntil}, true
			}
		}
	}
	return Claim{}, false
}

func (e *Engine) Claim(ctx context.Context, actor, requestID, runnerID, sliceID string) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		if runnerID == "" {
			return fail(CodeInvalid, "a claim needs a runner id")
		}
		u := d.s.Usage()
		lim := d.cfg.Limits
		fits := func(sl *Slice) error {
			if !sl.HoldsExecute && u.Execute >= lim.Execute {
				return fail(CodeNoCapacity, "Execute is at its limit of %d", lim.Execute)
			}
			if !sl.HoldsReview && u.Review >= lim.Review {
				return fail(CodeNoCapacity, "Review is at its limit of %d, counting reservations", lim.Review)
			}
			return nil
		}
		var chosen *Slice
		if sliceID != "" {
			chosen = d.s.Slices[sliceID]
			if chosen == nil {
				return errUnknown("slice", sliceID)
			}
			if !chosen.claimable() {
				return fail(CodeConflict, "slice %s is %s, not claimable", chosen.ID, chosen.Status)
			}
			if err := fits(chosen); err != nil {
				return err
			}
		} else {
			candidates := d.s.claimableByPriority()
			if len(candidates) == 0 {
				return fail(CodeNoWork, "no claimable slices")
			}
			var firstErr error
			for _, sl := range candidates {
				err := fits(sl)
				if err == nil {
					chosen = sl
					break
				}
				if firstErr == nil {
					firstErr = err
				}
			}
			if chosen == nil {
				return firstErr
			}
		}
		d.emit(EvSliceClaimed, sliceClaimed{
			RunID: d.nextRunID(), SliceID: chosen.ID, RunnerID: runnerID,
			LeaseUntil: d.now.Add(d.cfg.Lease), TakesExecute: !chosen.HoldsExecute, TakesReview: !chosen.HoldsReview,
		})
		return nil
	})
}

func (e *Engine) Heartbeat(ctx context.Context, actor, requestID, runID, note string) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		r, err := d.openRun(runID)
		if err != nil {
			return err
		}
		d.emit(EvHeartbeat, heartbeat{RunID: r.ID, LeaseUntil: d.now.Add(d.cfg.Lease), Note: note})
		return nil
	})
}

func (e *Engine) FlagRun(ctx context.Context, actor, requestID, runID string, ask AskType, question, owner string) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		r, err := d.openRun(runID)
		if err != nil {
			return err
		}
		if !ask.Valid() {
			return fail(CodeInvalid, "unknown ask type %q", ask)
		}
		if question == "" {
			return fail(CodeInvalid, "a flag needs a question")
		}
		sl := d.s.Slices[r.SliceID]
		if owner == "" {
			owner = d.s.Items[sl.ItemID].Owner
		}
		d.emit(EvRunClosed, runClosed{RunID: r.ID, Reason: CloseFlagged})
		d.raiseFlag(Subject{Kind: SubjectSlice, ID: sl.ID}, ask, owner, question)
		return nil
	})
}

func (e *Engine) Submit(ctx context.Context, actor, requestID, runID, branch string) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		r, err := d.openRun(runID)
		if err != nil {
			return err
		}
		if branch == "" {
			return fail(CodeInvalid, "submit needs the branch the work will be shipped to")
		}
		d.emit(EvRunClosed, runClosed{RunID: r.ID, Reason: CloseSubmitted, Branch: branch})
		return nil
	})
}

func (e *Engine) ReleaseRun(ctx context.Context, actor, requestID, runID string) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		r, err := d.openRun(runID)
		if err != nil {
			return err
		}
		d.emit(EvRunClosed, runClosed{RunID: r.ID, Reason: CloseReleased})
		return nil
	})
}

func (e *Engine) PROpened(ctx context.Context, actor, requestID, sliceID, repo string, number int, headSHA string) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		sl := d.s.Slices[sliceID]
		if sl == nil {
			return errUnknown("slice", sliceID)
		}
		if sl.Status == SliceMerged {
			return fail(CodeConflict, "slice %s has already merged", sl.ID)
		}
		if repo == "" || number <= 0 || headSHA == "" {
			return fail(CodeInvalid, "a PR needs a repo, a number and a head SHA")
		}
		d.emit(EvPROpened, prOpened{SliceID: sl.ID, Repo: repo, Number: number, HeadSHA: headSHA})
		return nil
	})
}

func (e *Engine) ChecksCompleted(ctx context.Context, actor, requestID, sliceID, headSHA string, success bool) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		sl, err := d.sliceWithPR(sliceID)
		if err != nil {
			return err
		}
		d.emit(EvChecksCompleted, checksCompleted{SliceID: sl.ID, HeadSHA: headSHA, Success: success})
		return nil
	})
}

func (e *Engine) ReadyForReview(ctx context.Context, actor, requestID, sliceID string) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		sl, err := d.sliceWithPR(sliceID)
		if err != nil {
			return err
		}
		if sl.Status != SlicePROpen {
			return fail(CodeConflict, "slice %s is %s; it moves into review from pr_open", sl.ID, sl.Status)
		}
		if !sl.PR.ChecksGreen {
			return fail(CodeConflict, "slice %s has no green checks on %s", sl.ID, sl.PR.HeadSHA)
		}
		d.emit(EvPRReadyForReview, sliceRef{SliceID: sl.ID})
		return nil
	})
}

func (e *Engine) PRApproved(ctx context.Context, actor, requestID, sliceID, headSHA, by string) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		sl, err := d.sliceWithPR(sliceID)
		if err != nil {
			return err
		}
		if sl.Status != SliceInReview {
			return fail(CodeConflict, "slice %s is %s, not in review", sl.ID, sl.Status)
		}
		if headSHA != sl.PR.HeadSHA {
			return fail(CodeConflict, "approval is for %s but the PR head is %s", headSHA, sl.PR.HeadSHA)
		}
		allApproved := true
		for _, sib := range d.s.itemSlices(sl.ItemID) {
			if sib.ID != sl.ID && sib.Status != SliceApproved && sib.Status != SliceMerged {
				allApproved = false
			}
		}
		switch {
		case !allApproved:
			d.emit(EvPRApproved, prApproved{SliceID: sl.ID, HeadSHA: headSHA, By: by})
		case d.s.Usage().Release < d.cfg.Limits.Release:
			d.emit(EvPRApproved, prApproved{SliceID: sl.ID, HeadSHA: headSHA, By: by, KeepsReview: true})
			d.emit(EvReleaseSlotTaken, itemRef{ItemID: sl.ItemID})
		default:
			d.emit(EvPRApproved, prApproved{SliceID: sl.ID, HeadSHA: headSHA, By: by, KeepsReview: true})
			d.emit(EvItemAwaitsRelease, itemRef{ItemID: sl.ItemID})
		}
		return nil
	})
}

func (e *Engine) PRChangesRequested(ctx context.Context, actor, requestID, sliceID, by string) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		sl, err := d.sliceWithPR(sliceID)
		if err != nil {
			return err
		}
		if sl.Status != SliceInReview {
			return fail(CodeConflict, "slice %s is %s, not in review", sl.ID, sl.Status)
		}
		d.emit(EvPRChangesRequested, prChangesRequested{SliceID: sl.ID, By: by})
		return nil
	})
}

func (e *Engine) PRMerged(ctx context.Context, actor, requestID, sliceID string) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		sl, err := d.sliceWithPR(sliceID)
		if err != nil {
			return err
		}
		if sl.Status == SliceMerged {
			return nil
		}
		d.emit(EvPRMerged, sliceRef{SliceID: sl.ID})
		return nil
	})
}

func (e *Engine) ReleaseDone(ctx context.Context, actor, requestID, itemID string) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		it := d.s.Items[itemID]
		if it == nil {
			return errUnknown("item", itemID)
		}
		if it.Status != ItemReleasing {
			return fail(CodeConflict, "item %s is %s, not releasing", it.ID, it.Status)
		}
		for _, sl := range d.s.itemSlices(it.ID) {
			if sl.Status != SliceMerged {
				return fail(CodeConflict, "slice %s of item %s has not merged", sl.ID, it.ID)
			}
		}
		d.emit(EvReleaseDone, itemRef{ItemID: it.ID})
		var waiting []*Item
		for _, w := range d.s.Items {
			if w.Status == ItemAwaitingRelease {
				waiting = append(waiting, w)
			}
		}
		if len(waiting) > 0 {
			sort.Slice(waiting, func(i, j int) bool {
				if waiting[i].Rank != waiting[j].Rank {
					return waiting[i].Rank < waiting[j].Rank
				}
				return waiting[i].ID < waiting[j].ID
			})
			d.emit(EvReleaseSlotTaken, itemRef{ItemID: waiting[0].ID})
		}
		return nil
	})
}

func (d *decider) tick() {
	parked := map[string]bool{}
	park := func(sl *Slice) {
		if !parked[sl.ID] {
			parked[sl.ID] = true
			d.emit(EvSliceParked, sliceRef{SliceID: sl.ID})
		}
	}
	runs := sortedRuns(d.s)
	for _, r := range runs {
		if r.Closed || d.now.Before(r.LeaseUntil) {
			continue
		}
		d.emit(EvRunClosed, runClosed{RunID: r.ID, Reason: CloseLeaseExpired})
		if !d.now.Before(r.LeaseUntil.Add(d.cfg.StallTimeout)) {
			park(d.s.Slices[r.SliceID])
		}
	}
	for _, sl := range sortedSlices(d.s) {
		if sl.Status == SliceStalled && !d.now.Before(sl.StalledAt.Add(d.cfg.StallTimeout)) {
			park(sl)
		}
	}
	for _, f := range sortedFlags(d.s) {
		if f.Closed || d.closedFlags[f.ID] || d.now.Before(f.TimeoutAt) || f.Ask != AskAnswer {
			continue
		}
		d.markClosed(f.ID)
		d.emit(EvFlagTimedOut, flagRef{FlagID: f.ID})
		switch f.Subject.Kind {
		case SubjectSlice:
			if sl := d.s.Slices[f.Subject.ID]; sl != nil && sl.Status == SliceFlagged {
				park(sl)
			}
		case SubjectItem:
			if it := d.s.Items[f.Subject.ID]; it != nil && it.Status == ItemPlanning && !parked[it.ID] {
				parked[it.ID] = true
				d.closeOpenFlags(f.Subject, "", "item parked")
				d.emit(EvItemParked, itemRef{ItemID: it.ID})
			}
		}
	}
}

func (d *decider) raiseFlag(subject Subject, ask AskType, owner, question string) {
	d.emit(EvFlagRaised, flagRaised{
		FlagID: d.nextFlagID(), Subject: subject, Ask: ask, Question: question,
		Owner: owner, TimeoutAt: d.now.Add(d.cfg.AskTimeout),
	})
}

func (d *decider) closeOpenFlags(subject Subject, ask AskType, answer string) {
	for _, f := range d.s.openFlagsFor(subject) {
		if d.closedFlags[f.ID] || (ask != "" && f.Ask != ask) {
			continue
		}
		d.markClosed(f.ID)
		d.emit(EvFlagResolved, flagResolved{FlagID: f.ID, Answer: answer})
	}
}

func (d *decider) markClosed(flagID string) {
	if d.closedFlags == nil {
		d.closedFlags = map[string]bool{}
	}
	d.closedFlags[flagID] = true
}

func (d *decider) planningItem(itemID string) (*Item, error) {
	it := d.s.Items[itemID]
	if it == nil {
		return nil, errUnknown("item", itemID)
	}
	if it.Status != ItemPlanning {
		return nil, fail(CodeConflict, "item %s is %s, not planning", it.ID, it.Status)
	}
	return it, nil
}

func (d *decider) openRun(runID string) (*Run, error) {
	r := d.s.Runs[runID]
	if r == nil {
		return nil, errUnknown("run", runID)
	}
	if r.Closed {
		return nil, fail(CodeConflict, "run %s is closed (%s)", r.ID, r.ClosedReason)
	}
	return r, nil
}

func (d *decider) sliceWithPR(sliceID string) (*Slice, error) {
	sl := d.s.Slices[sliceID]
	if sl == nil {
		return nil, errUnknown("slice", sliceID)
	}
	if sl.PR == nil {
		return nil, fail(CodeConflict, "slice %s has no PR", sl.ID)
	}
	return sl, nil
}

func sortedRuns(s *State) []*Run {
	out := make([]*Run, 0, len(s.Runs))
	for _, r := range s.Runs {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func sortedSlices(s *State) []*Slice {
	out := make([]*Slice, 0, len(s.Slices))
	for _, sl := range s.Slices {
		out = append(out, sl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func sortedFlags(s *State) []*Flag {
	out := make([]*Flag, 0, len(s.Flags))
	for _, f := range s.Flags {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
