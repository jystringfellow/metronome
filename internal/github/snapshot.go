package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type PRState string

const (
	PROpen   PRState = "OPEN"
	PRMerged PRState = "MERGED"
	PRClosed PRState = "CLOSED"
)

type Checks string

const (
	ChecksNone    Checks = "none"
	ChecksPending Checks = "pending"
	ChecksSuccess Checks = "success"
	ChecksFailure Checks = "failure"
)

type ReviewState string

const (
	ReviewApproved         ReviewState = "APPROVED"
	ReviewChangesRequested ReviewState = "CHANGES_REQUESTED"
	ReviewCommented        ReviewState = "COMMENTED"
	ReviewDismissed        ReviewState = "DISMISSED"
)

type Review struct {
	ID          string
	Author      string
	State       ReviewState
	CommitSHA   string
	SubmittedAt time.Time
}

type Snapshot struct {
	Repo       string
	Number     int
	HeadSHA    string
	HeadAuthor string
	Draft      bool
	State      PRState
	HeadChecks Checks
	Reviews    []Review
	MergedBy   string
}

type Source interface {
	PRForBranch(ctx context.Context, repo, branch string) (*Snapshot, error)
}

type GH struct {
	Run func(ctx context.Context, args ...string) ([]byte, error)
}

const prFields = "number,headRefOid,isDraft,state,updatedAt,mergedBy,reviews,statusCheckRollup,commits"

func (g GH) PRForBranch(ctx context.Context, repo, branch string) (*Snapshot, error) {
	run := g.Run
	if run == nil {
		run = runGH
	}
	out, err := run(ctx, "pr", "list", "--repo", repo, "--head", branch, "--state", "all", "--limit", "10", "--json", prFields)
	if err != nil {
		return nil, err
	}
	return ParsePRList(repo, out)
}

func runGH(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args[:2], " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

type ghPR struct {
	Number     int                     `json:"number"`
	HeadRefOid string                  `json:"headRefOid"`
	IsDraft    bool                    `json:"isDraft"`
	State      PRState                 `json:"state"`
	UpdatedAt  time.Time               `json:"updatedAt"`
	MergedBy   *struct{ Login string } `json:"mergedBy"`
	Commits    []struct {
		Oid     string `json:"oid"`
		Authors []struct {
			Login string `json:"login"`
		} `json:"authors"`
	} `json:"commits"`
	Reviews []struct {
		ID          string                 `json:"id"`
		Author      struct{ Login string } `json:"author"`
		State       ReviewState            `json:"state"`
		SubmittedAt time.Time              `json:"submittedAt"`
		Commit      struct{ Oid string }   `json:"commit"`
	} `json:"reviews"`
	StatusCheckRollup []struct {
		Typename     string    `json:"__typename"`
		Name         string    `json:"name"`
		WorkflowName string    `json:"workflowName"`
		Context      string    `json:"context"`
		Status       string    `json:"status"`
		Conclusion   string    `json:"conclusion"`
		State        string    `json:"state"`
		StartedAt    time.Time `json:"startedAt"`
	} `json:"statusCheckRollup"`
}

func ParsePRList(repo string, data []byte) (*Snapshot, error) {
	var prs []ghPR
	if err := json.Unmarshal(data, &prs); err != nil {
		return nil, fmt.Errorf("gh pr list output: %w", err)
	}
	if len(prs) == 0 {
		return nil, nil
	}
	sort.SliceStable(prs, func(i, j int) bool {
		if (prs[i].State == PROpen) != (prs[j].State == PROpen) {
			return prs[i].State == PROpen
		}
		return prs[i].UpdatedAt.After(prs[j].UpdatedAt)
	})
	return snapshotOf(repo, prs[0]), nil
}

func snapshotOf(repo string, p ghPR) *Snapshot {
	snap := &Snapshot{Repo: repo, Number: p.Number, HeadSHA: p.HeadRefOid, Draft: p.IsDraft, State: p.State, HeadChecks: ChecksNone}
	if p.MergedBy != nil {
		snap.MergedBy = p.MergedBy.Login
	}
	for _, commit := range p.Commits {
		if commit.Oid != p.HeadRefOid {
			continue
		}
		for _, author := range commit.Authors {
			if author.Login != "" {
				snap.HeadAuthor = author.Login
				break
			}
		}
		break
	}
	for _, r := range p.Reviews {
		id := r.ID
		if id == "" {
			id = r.Author.Login + "@" + r.Commit.Oid + "@" + r.SubmittedAt.Format(time.RFC3339)
		}
		snap.Reviews = append(snap.Reviews, Review{ID: id, Author: r.Author.Login, State: r.State, CommitSHA: r.Commit.Oid, SubmittedAt: r.SubmittedAt})
	}
	type run struct {
		result  Checks
		started time.Time
	}
	latest := map[string]run{}
	for _, c := range p.StatusCheckRollup {
		key := c.Typename + "/" + c.WorkflowName + "/" + c.Name + "/" + c.Context
		r := run{checkResult(c.Typename, c.Status, c.Conclusion, c.State), c.StartedAt}
		if prev, ok := latest[key]; !ok || !r.started.Before(prev.started) {
			latest[key] = r
		}
	}
	for _, r := range latest {
		snap.HeadChecks = worse(snap.HeadChecks, r.result)
	}
	return snap
}

func checkResult(typename, status, conclusion, state string) Checks {
	if typename == "StatusContext" {
		switch state {
		case "SUCCESS":
			return ChecksSuccess
		case "PENDING", "EXPECTED":
			return ChecksPending
		}
		return ChecksFailure
	}
	if status != "COMPLETED" {
		return ChecksPending
	}
	switch conclusion {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return ChecksSuccess
	}
	return ChecksFailure
}

func worse(a, b Checks) Checks {
	rank := map[Checks]int{ChecksNone: 0, ChecksSuccess: 1, ChecksPending: 2, ChecksFailure: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}
