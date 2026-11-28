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
	backlog := e.file("backlog.json", api.BacklogRequest{Items: []flow.BacklogItem{{Ref: "ACME-1", Title: "Add export", Owner: "pat", PredictedTier: intp(1)}}})
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

	snap := prs["metronome/I1.1"]
	snap.HeadChecks = github.ChecksFailure
	if out, code := e.poll(prs); code != 0 || !strings.Contains(out, "checks failed") {
		t.Fatalf("poll exited %d:\n%s", code, out)
	}
	snap.HeadSHA, snap.HeadChecks, snap.HeadAuthor = "cccccccc", github.ChecksSuccess, "sam"
	if out, code := e.poll(prs); code != 0 || !strings.Contains(out, "recorded I1.1 back in review at ccccccc, fixed outside a run by sam") {
		t.Fatalf("poll exited %d:\n%s", code, out)
	}
	snap.Reviews = []github.Review{{ID: "PRR_1", Author: "rev", State: github.ReviewApproved, CommitSHA: "cccccccc", SubmittedAt: time.Now()}}
	if out, code := e.poll(prs); code != 0 || !strings.Contains(out, "recorded I1.1 approved by rev at ccccccc") {
		t.Fatalf("poll exited %d:\n%s", code, out)
	}
	if out, code := e.poll(prs); code != 0 || strings.Contains(out, "recorded") {
		t.Fatalf("a repeat poll exited %d and recorded:\n%s", code, out)
	}
}

func TestGitHubReworkFixedCommand(t *testing.T) {
	e := newEnv(t)
	backlog := e.file("backlog.json", api.BacklogRequest{Items: []flow.BacklogItem{{Ref: "ACME-1", Title: "Add export", Owner: "pat", Slices: []flow.SliceSpec{{Title: "API endpoint", Repo: "acme/app"}}}}})
	e.ok("pat", "backlog", "sync", backlog)
	e.ok("agent", "plan", "start")
	e.ok("pat", "plan", "approve", "I1")
	e.ok("laptop", "next", "--runner", "laptop")
	e.ok("laptop", "submit", "R1", "--branch", "metronome/I1.1")
	e.ok("github", "github", "pr-opened", "--slice", "I1.1", "--repo", "acme/app", "--number", "7", "--head", "aaa")
	e.ok("github", "github", "checks", "--slice", "I1.1", "--head", "aaa", "--failure")
	out := e.ok("github", "github", "rework-fixed", "--slice", "I1.1", "--repo", "acme/app", "--number", "7", "--head", "ccc", "--by", "sam")
	for _, want := range []string{"pr_opened", "checks_completed", "rework_fixed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q lacks %q", out, want)
		}
	}
	if out := e.ok("github", "help"); !strings.Contains(out, "github rework-fixed") {
		t.Fatal(out)
	}
}

func TestGitHubPollRefusesANegativeInterval(t *testing.T) {
	e := newEnv(t)
	if out, code := e.poll(branchPRs{}, "--every", "-1m"); code != 2 || !strings.Contains(out, "--every") {
		t.Fatalf("poll --every -1m exited %d:\n%s", code, out)
	}
}
