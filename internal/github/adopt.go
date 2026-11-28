package github

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/flow"
)

var prURL = regexp.MustCompile(`^https://github\.com/([^/]+/[^/]+)/pull/([1-9][0-9]*)(?:/|/files|/commits|/checks)?$`)

func ParsePRRef(value string) (string, int, error) {
	if match := prURL.FindStringSubmatch(value); match != nil {
		value = match[1] + "#" + match[2]
	}
	refs, err := flow.NormalisePRs([]string{value})
	if err != nil {
		return "", 0, err
	}
	repo, number, _ := strings.Cut(refs[0], "#")
	n, err := strconv.Atoi(number)
	return repo, n, err
}

func FormatPRRef(repo string, number int) string {
	return fmt.Sprintf("%s#%d", strings.ToLower(repo), number)
}

type PullRequest struct {
	Snapshot
	Title, Body, Author, HeadRef, HeadRepo, URL string
	CrossRepository                             bool
	Labels                                      []string
}

type LabelledPR struct {
	Number          int
	CrossRepository bool
	Labels          []string
}

type PRSource interface {
	PR(context.Context, string, int) (*PullRequest, error)
	LabelledPRs(context.Context, string, string) ([]LabelledPR, error)
}

func ParsePR(repo string, data []byte) (*PullRequest, error) {
	var raw struct {
		ghPR
		Title, Body, URL, HeadRefName string
		Author                        struct{ Login string }
		HeadRepository                struct{ Name string }
		HeadRepositoryOwner           struct{ Login string }
		IsCrossRepository             bool
		Labels                        []struct{ Name string }
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	pr := &PullRequest{Snapshot: *snapshotOf(repo, raw.ghPR), Title: raw.Title, Body: raw.Body, URL: raw.URL, Author: raw.Author.Login, HeadRef: raw.HeadRefName, CrossRepository: raw.IsCrossRepository}
	if raw.HeadRepository.Name != "" && raw.HeadRepositoryOwner.Login != "" {
		pr.HeadRepo = raw.HeadRepositoryOwner.Login + "/" + raw.HeadRepository.Name
	}
	for _, label := range raw.Labels {
		pr.Labels = append(pr.Labels, label.Name)
	}
	return pr, nil
}

func (g GH) PR(ctx context.Context, repo string, number int) (*PullRequest, error) {
	data, err := g.call(ctx, "pr", "view", strconv.Itoa(number), "--repo", repo, "--json", prFields+",title,body,author,headRefName,headRepository,headRepositoryOwner,isCrossRepository,labels,url")
	if err != nil {
		if strings.Contains(err.Error(), "Could not resolve to a PullRequest with the number of "+strconv.Itoa(number)+".") {
			return nil, nil
		}
		return nil, err
	}
	pr, err := ParsePR(repo, data)
	if err != nil {
		return nil, err
	}
	if pr.Number != number {
		return nil, fmt.Errorf("PR %s returned number %d", FormatPRRef(repo, number), pr.Number)
	}
	return pr, nil
}

func (g GH) LabelledPRs(ctx context.Context, repo, label string) ([]LabelledPR, error) {
	data, err := g.call(ctx, "pr", "list", "--repo", repo, "--label", label, "--state", "open", "--limit", "1000", "--json", "number,isCrossRepository,labels")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Number            int
		IsCrossRepository bool
		Labels            []struct{ Name string }
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("PR list for %s is not an array", repo)
	}
	if len(raw) >= 1000 {
		return nil, fmt.Errorf("PR list for %s may be truncated", repo)
	}
	var out []LabelledPR
	for _, entry := range raw {
		pr := LabelledPR{Number: entry.Number, CrossRepository: entry.IsCrossRepository}
		for _, label := range entry.Labels {
			pr.Labels = append(pr.Labels, label.Name)
		}
		out = append(out, pr)
	}
	return out, nil
}

func AdoptionFor(pr *PullRequest) (api.AdoptRequest, error) {
	var req api.AdoptRequest
	if pr == nil {
		return req, fmt.Errorf("PR not found")
	}
	ref := FormatPRRef(pr.Repo, pr.Number)
	if pr.State != PROpen {
		return req, fmt.Errorf("%s is %s; only open PRs can be adopted", ref, strings.ToLower(string(pr.State)))
	}
	if pr.CrossRepository || !strings.EqualFold(pr.Repo, pr.HeadRepo) {
		return req, fmt.Errorf("%s comes from a fork (%s); runners cannot push to it, so Metronome does not adopt it", ref, pr.HeadRepo)
	}
	if pr.Author == "" {
		return req, fmt.Errorf("%s has no author", ref)
	}
	tier, err := TierLabel(pr.Labels)
	if err != nil {
		return req, err
	}
	return api.AdoptRequest{Repo: pr.Repo, Number: pr.Number, HeadRepo: pr.HeadRepo, Branch: pr.HeadRef, HeadSHA: pr.HeadSHA, Author: pr.Author, Title: pr.Title, Intent: bodyIntent(pr.Body), Criteria: bodyCriteria(pr.Body), Tier: tier}, nil
}

func ChooseSourcePR(candidates []*PullRequest) (*PullRequest, error) {
	var open, nonDraft []*PullRequest
	for _, pr := range candidates {
		if pr != nil && pr.State == PROpen {
			open = append(open, pr)
			if !pr.Draft {
				nonDraft = append(nonDraft, pr)
			}
		}
	}
	if len(open) == 1 {
		return open[0], nil
	}
	if len(nonDraft) == 1 {
		return nonDraft[0], nil
	}
	if len(open) == 0 {
		return nil, fmt.Errorf("none of its PRs is open")
	}
	return nil, fmt.Errorf("names %d open PRs; a person must decide", len(open))
}
