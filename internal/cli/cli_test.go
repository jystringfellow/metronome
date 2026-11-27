package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/cli"
	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/store"
)

type env struct {
	t    *testing.T
	http *http.Client
	dir  string
}

type handlerTransport struct{ h http.Handler }

func (ht handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	ht.h.ServeHTTP(rec, r)
	return rec.Result(), nil
}

func newEnv(t *testing.T) *env {
	t.Helper()
	eng, err := flow.NewEngine(context.Background(), flow.DefaultConfig(), store.NewMemory(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, http: &http.Client{Transport: handlerTransport{api.Handler(eng)}}, dir: t.TempDir()}
}

func (e *env) run(as string, args ...string) (string, int) {
	e.t.Helper()
	var out, errOut bytes.Buffer
	code := cli.RunWithHTTP(context.Background(), append([]string{"--server", "http://metronome.test", "--as", as}, args...), &out, &errOut, e.http)
	return out.String() + errOut.String(), code
}

func (e *env) ok(as string, args ...string) string {
	e.t.Helper()
	out, code := e.run(as, args...)
	if code != 0 {
		e.t.Fatalf("metronome %s exited %d:\n%s", strings.Join(args, " "), code, out)
	}
	return out
}

func (e *env) file(name string, v any) string {
	e.t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		e.t.Fatal(err)
	}
	path := filepath.Join(e.dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		e.t.Fatal(err)
	}
	return path
}

func (e *env) board() api.Board {
	e.t.Helper()
	var b api.Board
	if err := json.Unmarshal([]byte(e.ok("pat", "board", "--json")), &b); err != nil {
		e.t.Fatal(err)
	}
	return b
}

func TestWorkstreamsFromCommandLine(t *testing.T) {
	e := newEnv(t)
	e.ok("pat", "workstream", "create", "acme", "--execute", "0")
	if out := e.ok("pat", "workstream", "list"); !strings.Contains(out, "0/0") || !strings.Contains(out, "default") {
		t.Fatal(out)
	}
	e.ok("pat", "workstream", "set", "acme", "--execute", "2")
	var views []flow.WorkstreamView
	if err := json.Unmarshal([]byte(e.ok("pat", "workstream", "list", "--json")), &views); err != nil || len(views) != 2 || views[0].Name != "acme" || views[0].Limits.Execute != 2 {
		t.Fatalf("views = %+v, err = %v", views, err)
	}
	if out, code := e.run("pat", "workstream", "set", "acme"); code != 2 {
		t.Fatal(out, code)
	}
	if out, code := e.run("pat", "workstream", "set", "missing", "--execute", "2"); code != 1 || !strings.Contains(out, "not_found") {
		t.Fatal(out, code)
	}
	for _, ws := range []string{"acme", "default"} {
		file := e.file(ws+".json", api.BacklogRequest{Items: []flow.BacklogItem{{Ref: ws, Title: ws + " item", Owner: "pat"}}})
		if ws == "default" {
			e.ok("pat", "backlog", "sync", file)
		} else {
			e.ok("pat", "backlog", "sync", "--workstream", ws, file)
		}
	}
	e.ok("pat", "plan", "start", "--workstream", "acme")
	slices := e.file("slices.json", []flow.SliceSpec{{Title: "example", Repo: "acme/app", Approvers: []string{"rev"}}})
	e.ok("pat", "plan", "propose", "I1", "--slices", slices)
	e.ok("pat", "plan", "approve", "I1")
	if out := e.ok("pat", "next", "--runner", "laptop", "--workstream", "acme"); !strings.Contains(out, "workstream: acme") {
		t.Fatal(out)
	}
	var board api.Board
	if err := json.Unmarshal([]byte(e.ok("pat", "board", "--workstream", "acme", "--json")), &board); err != nil || len(board.Items) != 1 || board.Items[0].Workstream != "acme" || len(board.Slices) != 1 || board.Slices[0].Workstream != "acme" || len(board.Workstreams) != 1 || len(board.Runs) != 1 {
		t.Fatalf("board = %+v, err = %v", board, err)
	}
	if out := e.ok("pat", "board"); !strings.Contains(out, "workstream acme") || !strings.Contains(out, "workstream default") || !strings.Contains(out, "default item") {
		t.Fatal(out)
	}
}

func TestOneItemThroughTheFlowFromTheCommandLine(t *testing.T) {
	e := newEnv(t)
	backlog := e.file("backlog.json", api.BacklogRequest{Items: []flow.BacklogItem{
		{Ref: "ACME-1", Title: "Add export", Owner: "pat", PredictedTier: 1},
	}})
	e.ok("pat", "backlog", "sync", backlog)
	e.ok("agent", "plan", "start")
	slices := e.file("slices.json", []flow.SliceSpec{{Title: "API endpoint", Repo: "acme/app", Approvers: []string{"rev"}}})
	e.ok("agent", "plan", "propose", "I1", "--slices", slices)
	if out := e.ok("pat", "inbox", "pat"); !strings.Contains(out, "Approve the plan for Add export") {
		t.Fatalf("inbox:\n%s", out)
	}
	e.ok("pat", "plan", "approve", "I1", "--tier", "2")

	if out := e.ok("laptop", "next", "--runner", "laptop"); !strings.Contains(out, "run R1 claimed slice I1.1") {
		t.Fatalf("next:\n%s", out)
	}
	e.ok("laptop", "report", "R1", "--note", "tests written")
	e.ok("laptop", "ask", "--run", "R1", "--type", "answer", "--question", "CSV or JSON?")
	if out, code := e.run("laptop", "report", "R1"); code != 1 || !strings.Contains(out, "conflict") {
		t.Fatalf("report on a flagged run exited %d:\n%s", code, out)
	}
	e.ok("pat", "answer", "F2", "CSV")
	e.ok("cloud", "next", "--runner", "cloud")
	e.ok("cloud", "submit", "R2", "--branch", "metronome/I1.1")

	e.ok("github", "github", "pr-opened", "--slice", "I1.1", "--repo", "acme/app", "--number", "42", "--head", "abc")
	e.ok("github", "github", "checks", "--slice", "I1.1", "--head", "abc", "--success")
	e.ok("github", "github", "ready-for-review", "--slice", "I1.1")
	if u := e.board().Usage; u.Execute != 0 || u.Review != 1 {
		t.Fatalf("usage in review = %+v", u)
	}
	e.ok("github", "github", "approved", "--slice", "I1.1", "--head", "abc", "--by", "rev")
	e.ok("github", "github", "merged", "--slice", "I1.1")
	e.ok("pat", "release-done", "I1")

	b := e.board()
	if b.Items[0].Status != flow.ItemDone || b.Usage.Review != 0 || b.Usage.Release != 0 {
		t.Fatalf("final board = %+v", b)
	}
	if out := e.ok("pat", "events"); strings.Count(out, "\n") < 15 {
		t.Fatalf("event log has too few entries:\n%s", out)
	}
}

func TestLowTierItemIsDoneAtMergeFromTheCommandLine(t *testing.T) {
	e := newEnv(t)
	backlog := e.file("backlog.json", api.BacklogRequest{Items: []flow.BacklogItem{{Ref: "ACME-1", Title: "Update docs", Owner: "pat", PredictedTier: 1}}})
	e.ok("pat", "backlog", "sync", backlog)
	e.ok("pat", "plan", "start")
	slices := e.file("slices.json", []flow.SliceSpec{{Title: "Docs", Repo: "acme/app"}})
	e.ok("pat", "plan", "propose", "I1", "--slices", slices)
	e.ok("pat", "plan", "approve", "I1", "--tier", "1")
	e.ok("laptop", "next", "--runner", "laptop")
	e.ok("laptop", "submit", "R1", "--branch", "metronome/I1.1")
	e.ok("github", "github", "pr-opened", "--slice", "I1.1", "--repo", "acme/app", "--number", "42", "--head", "abc")
	e.ok("github", "github", "merged", "--slice", "I1.1", "--by", "pat")
	if board := e.board(); board.Items[0].Status != flow.ItemDone || board.Usage.Execute != 0 || board.Usage.Review != 0 || board.Usage.Release != 0 {
		t.Fatalf("final board = %+v", board)
	}
	if out, code := e.run("pat", "release-done", "I1"); code != 1 || !strings.Contains(out, "conflict") || !strings.Contains(out, "tier 1") || !strings.Contains(out, "last slice merges") {
		t.Fatalf("release-done exited %d:\n%s", code, out)
	}
}

func TestLowTierNonFinalMergeFreesReviewFromTheCommandLine(t *testing.T) {
	for _, tier := range []string{"0", "1"} {
		t.Run("tier "+tier, func(t *testing.T) {
			e := newEnv(t)
			backlog := e.file("backlog.json", api.BacklogRequest{Items: []flow.BacklogItem{{Ref: "ACME-1", Title: "Example", Owner: "pat"}}})
			e.ok("pat", "backlog", "sync", backlog)
			e.ok("pat", "plan", "start")
			slices := e.file("slices.json", []flow.SliceSpec{{Title: "First", Repo: "acme/app"}, {Title: "Second", Repo: "acme/app"}})
			e.ok("pat", "plan", "propose", "I1", "--slices", slices)
			e.ok("pat", "plan", "approve", "I1", "--tier", tier)
			for _, slice := range []struct{ id, run, number string }{{"I1.1", "R1", "41"}, {"I1.2", "R2", "42"}} {
				e.ok("laptop", "next", "--runner", "laptop", "--slice", slice.id)
				e.ok("laptop", "submit", slice.run, "--branch", "metronome/"+slice.id)
				e.ok("github", "github", "pr-opened", "--slice", slice.id, "--repo", "acme/app", "--number", slice.number, "--head", "abc")
				e.ok("github", "github", "checks", "--slice", slice.id, "--head", "abc", "--success")
				e.ok("github", "github", "ready-for-review", "--slice", slice.id)
				e.ok("github", "github", "approved", "--slice", slice.id, "--head", "abc", "--by", "rev")
			}
			if board := e.board(); board.Usage.Review != 1 || board.Usage.Release != 0 {
				t.Fatalf("board before merge = %+v", board)
			}
			e.ok("github", "github", "merged", "--slice", "I1.2", "--by", "rev")
			board := e.board()
			if board.Items[0].Status != flow.ItemExecuting || board.Usage.Review != 0 || board.Usage.Execute != 0 || board.Usage.Release != 0 {
				t.Fatalf("board after non-final merge = %+v", board)
			}
			for _, slice := range board.Slices {
				if slice.ID == "I1.2" && (slice.Status != flow.SliceMerged || slice.HoldsReview) {
					t.Fatalf("merged slice = %+v", slice)
				}
			}
			e.ok("github", "github", "merged", "--slice", "I1.1", "--by", "rev")
			if board := e.board(); board.Items[0].Status != flow.ItemDone || board.Usage.Review != 0 || board.Usage.Release != 0 {
				t.Fatalf("final board = %+v", board)
			}
		})
	}
}

func TestRequestIDMakesARepeatedClaimSafe(t *testing.T) {
	e := newEnv(t)
	backlog := e.file("backlog.json", api.BacklogRequest{Items: []flow.BacklogItem{{Ref: "ACME-1", Title: "x", Owner: "pat"}}})
	e.ok("pat", "backlog", "sync", backlog)
	e.ok("pat", "plan", "start")
	slices := e.file("slices.json", []flow.SliceSpec{{Title: "a", Repo: "acme/app"}, {Title: "b", Repo: "acme/app"}})
	e.ok("pat", "plan", "propose", "I1", "--slices", slices)
	e.ok("pat", "plan", "approve", "I1")
	first := e.ok("laptop", "--request-id", "q1", "next", "--runner", "laptop")
	again := e.ok("laptop", "--request-id", "q1", "next", "--runner", "laptop")
	if first != again {
		t.Fatalf("repeated claim:\n%s\nvs\n%s", first, again)
	}
	if u := e.board().Usage; u.Execute != 1 {
		t.Fatalf("execute usage = %d, want 1", u.Execute)
	}
}

func TestErrorsExitNonZeroWithTheirCode(t *testing.T) {
	e := newEnv(t)
	if out, code := e.run("laptop", "next", "--runner", "laptop"); code != 1 || !strings.Contains(out, "no_work") {
		t.Fatalf("next with no work exited %d:\n%s", code, out)
	}
	if out, code := e.run("laptop", "next"); code != 2 || !strings.Contains(out, "--runner") {
		t.Fatalf("next without a runner exited %d:\n%s", code, out)
	}
}
