package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/flow"
)

type Poller struct {
	Client *api.Client
	Source Source
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
		snap, err := p.Source.PRForBranch(ctx, sl.Repo, sl.Branch)
		if err != nil {
			res.Errors = append(res.Errors, sl.ID+": "+err.Error())
			continue
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
	return res, nil
}
