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

type adoptionPRs map[int]*github.PullRequest

func (prs adoptionPRs) PR(_ context.Context, _ string, number int) (*github.PullRequest, error) {
	return prs[number], nil
}
func (prs adoptionPRs) LabelledPRs(_ context.Context, _, label string) ([]github.LabelledPR, error) {
	var result []github.LabelledPR
	for _, pr := range prs {
		for _, name := range pr.Labels {
			if name == label {
				result = append(result, github.LabelledPR{Number: pr.Number, CrossRepository: pr.CrossRepository, Labels: pr.Labels})
				break
			}
		}
	}
	return result, nil
}

func adoptablePR() *github.PullRequest {
	return &github.PullRequest{Snapshot: github.Snapshot{Repo: "acme/app", Number: 12, HeadSHA: "abc", State: github.PROpen, HeadChecks: github.ChecksSuccess}, Title: "Export", Author: "pat", HeadRef: "feature-x", HeadRepo: "acme/app", Labels: []string{"metronome:adopt"}}
}

func (e *env) adoptionCommand(prs adoptionPRs, args ...string) (string, int) {
	e.t.Helper()
	var out, errOut bytes.Buffer
	code := cli.RunWithDeps(e.t.Context(), append([]string{"--server", "http://metronome.test", "--as", "github", "github"}, args...), &out, &errOut, cli.Deps{HTTP: e.http, PullRequests: prs})
	return out.String() + errOut.String(), code
}

func TestGitHubAdoptCommand(t *testing.T) {
	e := newEnv(t)
	e.ok("pat", "workstream", "create", "example")
	pr := adoptablePR()
	prs := adoptionPRs{12: pr}
	out, code := e.adoptionCommand(prs, "adopt", "acme/app#12", "--workstream", "example")
	if code != 0 || !strings.Contains(out, "adopted as I1") || !strings.Contains(out, "ready-for-review") {
		t.Fatal(code, out)
	}
	board := e.board()
	if len(board.Items) != 1 || board.Items[0].Owner != "pat" || board.Items[0].Workstream != "example" || board.Slices[0].Status != flow.SliceInReview {
		t.Fatal(board)
	}
	if out, code = e.adoptionCommand(prs, "adopt", "acme/app#12"); code != 0 || !strings.Contains(out, "already followed") {
		t.Fatal(code, out)
	}
	pr = adoptablePR()
	pr.Number = 13
	pr.HeadRepo = "other/app"
	prs[13] = pr
	if out, code = e.adoptionCommand(prs, "adopt", "acme/app#13"); code != 1 || !strings.Contains(out, "fork") {
		t.Fatal(code, out)
	}
	pr.HeadRepo = "acme/app"
	pr.State = github.PRMerged
	if out, code = e.adoptionCommand(prs, "adopt", "acme/app#13"); code != 1 || !strings.Contains(out, "only open PRs") {
		t.Fatal(code, out)
	}
	if out, code = e.adoptionCommand(prs, "adopt", "bad"); code != 2 {
		t.Fatal(code, out)
	}
}

func TestAdoptionCLIHelpAndWorkstreamFlags(t *testing.T) {
	e := newEnv(t)
	for _, want := range []string{"github adopt", "--adopt-repo", "--adopt-label"} {
		if out := e.ok("pat", "help"); !strings.Contains(out, want) {
			t.Fatal(out)
		}
	}
	e.ok("pat", "workstream", "set", "default", "--adopt-label", "adopt")
	if out := e.ok("pat", "workstream", "list"); !strings.Contains(out, "ADOPT LABEL") || !strings.Contains(out, "adopt") {
		t.Fatal(out)
	}
	if out, code := e.run("pat", "workstream", "set", "default", "--adopt-label", "other", "--review", "3"); code != 2 || !strings.Contains(out, "not both") {
		t.Fatal(code, out)
	}
	e.ok("pat", "workstream", "set", "default", "--adopt-label", "")
	if out := e.ok("pat", "workstream", "list", "--json"); strings.Contains(out, "adopt_label") {
		t.Fatal(out)
	}
	if out, code := e.adoptionCommand(adoptionPRs{}, "poll", "--adopt-repo", "bad"); code != 2 || !strings.Contains(out, "--adopt-repo") {
		t.Fatal(code, out)
	}
}

func TestAdoptionCLIBranchOnlySourceDoesNotUseGH(t *testing.T) {
	e := newEnv(t)
	backlog := e.file("backlog.json", api.BacklogRequest{Items: []flow.BacklogItem{{Ref: "ACME-1", Title: "Export", Owner: "pat", PRs: []string{"acme/app#12"}}}})
	e.ok("pat", "backlog", "sync", backlog)
	t.Setenv("PATH", t.TempDir())
	if out, code := e.poll(branchPRs{}); code != 0 || strings.Contains(out, "recorded") {
		t.Fatal(code, out)
	}
	var out, notes bytes.Buffer
	code := cli.RunWithDeps(t.Context(), []string{"--server", "http://metronome.test", "github", "adopt", "acme/app#12"}, &out, &notes, cli.Deps{HTTP: e.http, GitHub: branchPRs{}})
	if code != 1 || !strings.Contains(notes.String(), "GitHub PR source is unavailable") {
		t.Fatal(code, out.String(), notes.String())
	}
}

func TestAdoptedTextBrief(t *testing.T) {
	e := newEnv(t)
	pr := adoptablePR()
	pr.HeadChecks = github.ChecksFailure
	if out, code := e.adoptionCommand(adoptionPRs{12: pr}, "adopt", "acme/app#12"); code != 0 {
		t.Fatal(code, out)
	}
	text := e.ok("sam", "next", "--runner", "example", "--slice", "I1.1")
	if !strings.Contains(text, "adopted:   PR acme/app#12 by pat on branch feature-x") || !strings.Contains(text, "bring it up to date with the branch's latest commits first, and ask if that is not clean") {
		t.Fatal(text)
	}
}

func TestGitHubPollAdoptsLabelAndSource(t *testing.T) {
	e := newEnv(t)
	e.ok("pat", "workstream", "create", "example")
	e.ok("pat", "workstream", "set", "example", "--adopt-label", "metronome:adopt")
	if out := e.ok("pat", "workstream", "list", "--json"); !strings.Contains(out, `"adopt_label": "metronome:adopt"`) {
		t.Fatal(out)
	}
	pr := adoptablePR()
	prs := adoptionPRs{12: pr}
	if out, code := e.adoptionCommand(prs, "poll", "--adopt-repo", "acme/app"); code != 0 || !strings.Contains(out, "adopted as I1") || !strings.Contains(out, "ready-for-review") {
		t.Fatal(code, out)
	}
	if out, code := e.adoptionCommand(prs, "poll", "--adopt-repo", "acme/app"); code != 0 || strings.Contains(out, "recorded") {
		t.Fatal(code, out)
	}
	draft := adoptablePR()
	draft.Number = 13
	draft.Draft = true
	draft.HeadRef = "draft-x"
	draft.Labels = nil
	prs[13] = draft
	source := adoptablePR()
	source.Number = 14
	source.HeadRef = "source-x"
	source.Labels = nil
	prs[14] = source
	backlog := e.file("backlog.json", api.BacklogRequest{Items: []flow.BacklogItem{{Ref: "ACME-1", Title: "Export", Owner: "sam", PRs: []string{"acme/app#13", "acme/app#14"}}}})
	e.ok("sam", "backlog", "sync", backlog)
	if out, code := e.adoptionCommand(prs, "poll"); code != 0 || !strings.Contains(out, "acme/app#14 adopted as I2") {
		t.Fatal(code, out)
	}
	if board := e.board(); board.Slices[1].PR.Number != 14 || board.Slices[1].Status != flow.SliceInReview || board.Items[1].Owner != "sam" {
		t.Fatal(board)
	}
	source.Reviews = []github.Review{{ID: "review-1", Author: "sam", State: github.ReviewChangesRequested, CommitSHA: "abc", SubmittedAt: time.Now()}}
	if out, code := e.adoptionCommand(prs, "poll"); code != 0 || !strings.Contains(out, "changes requested") {
		t.Fatal(code, out)
	}
	text := e.ok("sam", "next", "--runner", "example", "--slice", "I2.1", "--json")
	if !strings.Contains(text, `"adopted": {`) || !strings.Contains(text, `"branch": "source-x"`) {
		t.Fatal(text)
	}
	e.ok("sam", "drop", "R1")
	source.HeadSHA = "def"
	source.HeadAuthor = "pat"
	if out, code := e.adoptionCommand(prs, "poll"); code != 0 || !strings.Contains(out, "fixed outside a run") {
		t.Fatal(code, out)
	}
	source.State = github.PRMerged
	source.MergedBy = "pat"
	if out, code := e.adoptionCommand(prs, "poll"); code != 0 || !strings.Contains(out, "merged by pat") {
		t.Fatal(code, out)
	}
	if board := e.board(); board.Items[1].Status != flow.ItemDone {
		t.Fatal(board.Items)
	}
}
