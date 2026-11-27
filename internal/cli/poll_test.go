package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/cli"
	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/github"
)

type branchPRs map[string]*github.Snapshot

func (b branchPRs) PRForBranch(_ context.Context, _, branch string) (*github.Snapshot, error) {
	return b[branch], nil
}

func (e *env) poll(prs branchPRs, args ...string) (string, int) {
	e.t.Helper()
	var out, errOut bytes.Buffer
	code := cli.RunWithDeps(context.Background(),
		append([]string{"--server", "http://metronome.test", "--as", "github", "github", "poll"}, args...),
		&out, &errOut, cli.Deps{HTTP: e.http, GitHub: prs})
	return out.String() + errOut.String(), code
}

func TestGitHubPollMovesASliceThroughReviewFromTheCommandLine(t *testing.T) {
	e := newEnv(t)
	backlog := e.file("backlog.json", api.BacklogRequest{Items: []flow.BacklogItem{{Ref: "ACME-1", Title: "Add export", Owner: "pat", PredictedTier: 1}}})
	e.ok("pat", "backlog", "sync", backlog)
	e.ok("agent", "plan", "start")
	slices := e.file("slices.json", []flow.SliceSpec{{Title: "API endpoint", Repo: "acme/app", Approvers: []string{"rev"}}})
	e.ok("agent", "plan", "propose", "I1", "--slices", slices)
	e.ok("pat", "plan", "approve", "I1", "--tier", "1")
	e.ok("laptop", "next", "--runner", "laptop")
	e.ok("laptop", "submit", "R1", "--branch", "metronome/I1.1")

	prs := branchPRs{"metronome/I1.1": {Repo: "acme/app", Number: 42, HeadSHA: "abc1234567", State: github.PROpen, HeadChecks: github.ChecksSuccess}}
	out, code := e.poll(prs)
	if code != 0 {
		t.Fatalf("poll exited %d:\n%s", code, out)
	}
	for _, want := range []string{"recorded I1.1 pr-opened acme/app#42 at abc1234", "recorded I1.1 checks green at abc1234", "recorded I1.1 ready-for-review"} {
		if !strings.Contains(out, want) {
			t.Fatalf("poll output lacks %q:\n%s", want, out)
		}
	}
	if u := e.board().Usage; u.Execute != 0 || u.Review != 1 {
		t.Fatalf("usage in review = %+v", u)
	}

	prs["metronome/I1.1"].Reviews = []github.Review{{ID: "PRR_1", Author: "rev", State: github.ReviewApproved, CommitSHA: "abc1234567", SubmittedAt: time.Now()}}
	if out, code := e.poll(prs); code != 0 || !strings.Contains(out, "recorded I1.1 approved by rev at abc1234") {
		t.Fatalf("poll exited %d:\n%s", code, out)
	}
	if out, code := e.poll(prs); code != 0 || strings.Contains(out, "recorded") {
		t.Fatalf("a repeat poll exited %d and recorded:\n%s", code, out)
	}
}

func TestGitHubPollRefusesANegativeInterval(t *testing.T) {
	e := newEnv(t)
	if out, code := e.poll(branchPRs{}, "--every", "-1m"); code != 2 || !strings.Contains(out, "--every") {
		t.Fatalf("poll --every -1m exited %d:\n%s", code, out)
	}
}
