package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/flow"
)

type Poller struct {
	Client     *api.Client
	Source     Source
	Adopt      PRSource
	AdoptRepos []string
}

type PollResult struct {
	Recorded []Action
	Notes    []string
	Errors   []string
}

func (p Poller) Once(ctx context.Context) (PollResult, error) {
	var res PollResult
	var b api.Board
	if err := p.Client.Get(ctx, "/v1/board", &b); err != nil {
		return res, err
	}
	if p.Adopt != nil {
		p.adoptAll(ctx, b, &res)
		if len(res.Recorded) > 0 {
			if err := p.Client.Get(ctx, "/v1/board", &b); err != nil {
				return res, err
			}
		}
	}
	waiters := map[string][]string{}
	for _, sl := range b.Slices {
		for _, id := range sl.WaitsOn {
			waiters[id] = append(waiters[id], sl.ID)
		}
	}
	for _, sl := range b.Slices {
		if !Tracked(sl) {
			continue
		}
		p.follow(ctx, sl, waiters, &res)
	}
	return res, nil
}

func (p Poller) Follow(ctx context.Context, sliceID string) (PollResult, error) {
	var b api.Board
	var res PollResult
	if err := p.Client.Get(ctx, "/v1/board", &b); err != nil {
		return res, err
	}
	for _, sl := range b.Slices {
		if sl.ID == sliceID && Tracked(sl) {
			p.follow(ctx, sl, nil, &res)
			break
		}
	}
	return res, nil
}

func (p Poller) follow(ctx context.Context, sl flow.Slice, waiters map[string][]string, res *PollResult) {
	var snap *Snapshot
	var err error
	if sl.Adopted != nil && p.Adopt != nil {
		var pr *PullRequest
		pr, err = p.Adopt.PR(ctx, sl.Adopted.Repo, sl.Adopted.Number)
		if pr != nil {
			snap = &pr.Snapshot
		}
	} else {
		snap, err = p.Source.PRForBranch(ctx, sl.Repo, sl.Branch)
	}
	if err != nil {
		res.Errors = append(res.Errors, sl.ID+": "+err.Error())
		return
	}
	actions, notes := Reconcile(sl, snap)
	res.Notes = append(res.Notes, notes...)
	merged := false
	for _, a := range actions {
		var out flow.Result
		if err := p.Client.Post(ctx, "/v1/github/"+a.Event, a.RequestID, a.Request, &out); err != nil {
			res.Errors = append(res.Errors, a.String()+": "+err.Error())
			break
		}
		if out.Duplicate && a.Event == "changes-requested" {
			res.Notes = append(res.Notes, a.Request.SliceID+": the change request from "+a.Request.By+" on this head was already acted on; waiting for them to review again")
		}
		if !out.Duplicate && len(out.Events) > 0 {
			res.Recorded = append(res.Recorded, a)
		}
		if a.Event == "merged" {
			merged = true
		}
	}
	if snap != nil && snap.State != PROpen && !merged {
		for _, id := range waiters[sl.ID] {
			res.Notes = append(res.Notes, fmt.Sprintf("%s cannot be claimed: it waits on %s, whose PR %s#%d is %s on GitHub but not merged in Metronome; a person must decide", id, sl.ID, snap.Repo, snap.Number, strings.ToLower(string(snap.State))))
		}
	}
}

func (p Poller) adoptAll(ctx context.Context, b api.Board, res *PollResult) {
	known := map[string]string{}
	for _, sl := range b.Slices {
		if sl.PR != nil {
			known[FormatPRRef(sl.PR.Repo, sl.PR.Number)] = sl.ID
		}
	}
	adopt := func(req api.AdoptRequest) {
		ref := FormatPRRef(req.Repo, req.Number)
		id := "github:adopt:" + ref
		if req.ItemID != "" {
			id = "github:adopt:" + req.ItemID + ":" + ref
		}
		action, notes, err := p.AdoptOne(ctx, req, id)
		res.Notes = append(res.Notes, notes...)
		if err != nil {
			var refusal *flow.Error
			if errors.As(err, &refusal) && (refusal.Code == flow.CodeConflict || refusal.Code == flow.CodeInvalid || refusal.Code == flow.CodeNoCapacity) {
				res.Notes = append(res.Notes, err.Error())
			} else {
				res.Errors = append(res.Errors, err.Error())
			}
			return
		}
		if action != nil {
			res.Recorded = append(res.Recorded, *action)
			known[ref] = action.Request.SliceID
		}
	}
	for _, it := range b.Items {
		if it.Status != flow.ItemReady || len(it.PRs) == 0 {
			continue
		}
		var candidates []*PullRequest
		failed := false
		for _, ref := range it.PRs {
			if id := known[ref]; id != "" {
				res.Notes = append(res.Notes, fmt.Sprintf("%s (%s) names %s, which slice %s already follows; a person must decide", it.ID, it.Ref, ref, id))
				failed = true
				break
			}
			repo, num, err := ParsePRRef(ref)
			if err != nil {
				res.Errors = append(res.Errors, err.Error())
				failed = true
				break
			}
			pr, err := p.Adopt.PR(ctx, repo, num)
			if err != nil {
				res.Errors = append(res.Errors, it.ID+": "+err.Error())
				failed = true
				break
			}
			candidates = append(candidates, pr)
		}
		if failed {
			continue
		}
		pr, err := ChooseSourcePR(candidates)
		if err != nil {
			res.Notes = append(res.Notes, it.ID+": "+err.Error())
			continue
		}
		if pr.CrossRepository || !strings.EqualFold(pr.Repo, pr.HeadRepo) {
			res.Notes = append(res.Notes, FormatPRRef(pr.Repo, pr.Number)+" comes from a fork; runners cannot push to it; not adopted")
			continue
		}
		adopt(api.AdoptRequest{ItemID: it.ID, Workstream: it.Workstream, Repo: pr.Repo, Number: pr.Number, HeadRepo: pr.HeadRepo, Branch: pr.HeadRef, HeadSHA: pr.HeadSHA, Author: pr.Author})
	}
	seen := map[string]bool{}
	for _, repo := range p.AdoptRepos {
		for _, ws := range b.Workstreams {
			if ws.AdoptLabel == "" {
				continue
			}
			prs, err := p.Adopt.LabelledPRs(ctx, repo, ws.AdoptLabel)
			if err != nil {
				res.Errors = append(res.Errors, err.Error())
				continue
			}
			for _, entry := range prs {
				ref := FormatPRRef(repo, entry.Number)
				if known[ref] != "" || seen[ref] {
					continue
				}
				seen[ref] = true
				matches := 0
				for _, stream := range b.Workstreams {
					if stream.AdoptLabel != "" {
						for _, label := range entry.Labels {
							if strings.EqualFold(label, stream.AdoptLabel) {
								matches++
								break
							}
						}
					}
				}
				if matches > 1 {
					res.Notes = append(res.Notes, ref+" has multiple workstream adoption labels; a person must decide")
					continue
				}
				if entry.CrossRepository {
					res.Notes = append(res.Notes, ref+" is labelled "+ws.AdoptLabel+" but comes from a fork; runners cannot push to it; not adopted")
					continue
				}
				pr, err := p.Adopt.PR(ctx, repo, entry.Number)
				if err != nil {
					res.Errors = append(res.Errors, err.Error())
					continue
				}
				req, err := AdoptionFor(pr)
				if err != nil {
					res.Notes = append(res.Notes, err.Error())
					continue
				}
				req.Workstream = ws.Name
				adopt(req)
			}
		}
	}
}

func (p Poller) AdoptOne(ctx context.Context, req api.AdoptRequest, requestID string) (*Action, []string, error) {
	var out flow.Result
	if err := p.Client.Post(ctx, "/v1/github/adopt", requestID, req, &out); err != nil {
		return nil, nil, err
	}
	for _, ev := range out.Events {
		if ev.Type != flow.EvPRAdopted {
			continue
		}
		var data struct {
			Workstream string `json:"workstream"`
			Owner      string `json:"owner"`
			Item       string `json:"item_id"`
			Slice      string `json:"slice_id"`
		}
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			return nil, nil, err
		}
		if out.Duplicate {
			return nil, []string{FormatPRRef(req.Repo, req.Number) + " is already followed by slice " + data.Slice}, nil
		}
		return &Action{Event: "adopted", Request: api.GitHubRequest{SliceID: data.Slice, Repo: req.Repo, Number: req.Number, By: req.Author}, AdoptionText: fmt.Sprintf("%s adopted as %s (slice %s) in workstream %s, owned by %s", FormatPRRef(req.Repo, req.Number), data.Item, data.Slice, data.Workstream, data.Owner)}, out.Notes, nil
	}
	return nil, out.Notes, nil
}
