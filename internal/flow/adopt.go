package flow

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Adoption struct {
	Workstream string   `json:"workstream"`
	ItemID     string   `json:"item_id"`
	Repo       string   `json:"repo"`
	Number     int      `json:"number"`
	HeadRepo   string   `json:"head_repo"`
	Branch     string   `json:"branch"`
	HeadSHA    string   `json:"head_sha"`
	Author     string   `json:"author"`
	Title      string   `json:"title"`
	Intent     string   `json:"intent"`
	Criteria   []string `json:"criteria"`
	Tier       *int     `json:"tier"`
}

type AdoptedPR struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Branch string `json:"branch"`
	Author string `json:"author"`
}

type prAdopted struct {
	Workstream    string   `json:"workstream"`
	ItemID        string   `json:"item_id"`
	Ref           string   `json:"ref,omitempty"`
	Title         string   `json:"title,omitempty"`
	Intent        string   `json:"intent,omitempty"`
	Owner         string   `json:"owner,omitempty"`
	Rank          int      `json:"rank,omitempty"`
	ConfirmedTier int      `json:"confirmed_tier"`
	TierDefaulted bool     `json:"tier_defaulted,omitempty"`
	SliceID       string   `json:"slice_id"`
	SliceTitle    string   `json:"slice_title"`
	Criteria      []string `json:"criteria"`
	Approvers     []string `json:"approvers"`
	Repo          string   `json:"repo"`
	Number        int      `json:"number"`
	Branch        string   `json:"branch"`
	HeadSHA       string   `json:"head_sha"`
	Author        string   `json:"author"`
}

var prPattern = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9_.-]+#[1-9][0-9]*$`)

func NormalisePRs(refs []string) ([]string, error) {
	var out []string
	for _, ref := range refs {
		repo, number, _ := strings.Cut(ref, "#")
		if _, err := strconv.Atoi(number); err != nil || !prPattern.MatchString(ref) || strings.HasSuffix(repo, "/.") || strings.HasSuffix(repo, "/..") {
			return nil, fail(CodeInvalid, "invalid PR reference %q", ref)
		}
		ref = strings.ToLower(ref)
		if !slices.Contains(out, ref) {
			out = append(out, ref)
		}
	}
	if len(out) > 10 {
		return nil, fail(CodeInvalid, "at most 10 PR references are allowed")
	}
	sort.Strings(out)
	return out, nil
}

func (e *Engine) AdoptPR(ctx context.Context, actor, requestID string, a Adoption) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		ref := fmt.Sprintf("%s#%d", strings.ToLower(a.Repo), a.Number)
		if _, err := NormalisePRs([]string{ref}); err != nil {
			return err
		}
		if a.Branch == "" || a.HeadSHA == "" || (a.ItemID == "" && (a.Author == "" || a.Title == "")) {
			return fail(CodeInvalid, "adoption needs branch, head SHA, author and title")
		}
		if !strings.EqualFold(a.HeadRepo, a.Repo) {
			return fail(CodeInvalid, "%s comes from a fork (%s); runners cannot push to it, so Metronome does not adopt it", ref, a.HeadRepo)
		}
		for _, sl := range sortedSlices(d.s) {
			if sl.PR != nil && strings.EqualFold(sl.PR.Repo, a.Repo) && sl.PR.Number == a.Number {
				d.note("%s is already followed by slice %s", ref, sl.ID)
				return nil
			}
		}
		for _, sl := range sortedSlices(d.s) {
			if sl.Status != SliceMerged && strings.EqualFold(sl.Repo, a.Repo) && sl.Branch == a.Branch {
				return fail(CodeConflict, "branch %s of %s is already followed by slice %s", a.Branch, a.Repo, sl.ID)
			}
		}
		p := prAdopted{Workstream: a.Workstream, ItemID: a.ItemID, Repo: strings.ToLower(a.Repo), Number: a.Number, Branch: a.Branch, HeadSHA: a.HeadSHA, Author: a.Author, Criteria: []string{}, Approvers: []string{}}
		if a.ItemID == "" {
			if p.Workstream == "" {
				p.Workstream = DefaultWorkstream
			}
			p.Ref, p.Title, p.Intent, p.Owner, p.SliceTitle = ref, a.Title, a.Intent, a.Author, a.Title
			if a.Criteria != nil {
				p.Criteria = a.Criteria
			}
			p.ConfirmedTier, p.TierDefaulted = 1, a.Tier == nil
			if a.Tier != nil {
				p.ConfirmedTier = *a.Tier
			}
			if p.ConfirmedTier < 0 || p.ConfirmedTier > 3 {
				return fail(CodeInvalid, "tier must be 0 to 3")
			}
			for _, it := range d.s.Items {
				if strings.EqualFold(it.Ref, ref) {
					return fail(CodeConflict, "item %s already has ref %s", it.ID, ref)
				}
				if it.Workstream == p.Workstream && it.Rank > p.Rank {
					p.Rank = it.Rank
				}
			}
			p.Rank++
			p.ItemID = d.nextItemID()
		} else {
			it := d.s.Items[a.ItemID]
			if it == nil || it.Status != ItemReady || !slices.Contains(it.PRs, ref) {
				return fail(CodeConflict, "item %s must be ready and name %s", a.ItemID, ref)
			}
			if p.Workstream != "" && p.Workstream != it.Workstream {
				return fail(CodeConflict, "item %s is in workstream %s", it.ID, it.Workstream)
			}
			p.Workstream, p.ConfirmedTier, p.SliceTitle = it.Workstream, it.PredictedTier, it.Title
			p.Owner = it.Owner
			if len(it.PlannedSlices) > 0 {
				spec := it.PlannedSlices[0]
				p.SliceTitle = spec.Title
				p.Criteria = append([]string{}, spec.Criteria...)
				p.Approvers = append([]string{}, spec.Approvers...)
			}
		}
		ws := d.s.Workstreams[p.Workstream]
		if ws == nil {
			return errUnknown("workstream", p.Workstream)
		}
		if d.s.StageUsage(p.Workstream).Review >= ws.Limits.Review {
			return fail(CodeNoCapacity, "Review in workstream %s is at its limit of %d; %s waits to be adopted", p.Workstream, ws.Limits.Review, ref)
		}
		p.SliceID = p.ItemID + ".1"
		d.emit(EvPRAdopted, p)
		return nil
	})
}

type adoptLabelSet struct {
	Workstream string `json:"workstream"`
	Label      string `json:"label"`
}

func (e *Engine) SetAdoptLabel(ctx context.Context, actor, requestID, ws, label string) (Result, error) {
	return e.execute(ctx, actor, requestID, func(d *decider) error {
		label = strings.TrimSpace(label)
		stream := d.s.Workstreams[ws]
		if stream == nil {
			return errUnknown("workstream", ws)
		}
		if utf8.RuneCountInString(label) > 50 || strings.Contains(label, ",") {
			return fail(CodeInvalid, "adoption label must have at most 50 characters and no comma")
		}
		for name, other := range d.s.Workstreams {
			if name != ws && label != "" && strings.EqualFold(other.AdoptLabel, label) {
				return fail(CodeConflict, "adoption label %q already used by %s", label, name)
			}
		}
		if stream.AdoptLabel != label {
			d.emit(EvWorkstreamAdoptLabelSet, adoptLabelSet{ws, label})
		}
		return nil
	})
}
