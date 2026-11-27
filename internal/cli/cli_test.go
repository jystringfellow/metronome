package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
	out, errOut, code := e.runOutput(as, args...)
	return out + errOut, code
}

func (e *env) runOutput(as string, args ...string) (string, string, int) {
	e.t.Helper()
	var out, errOut bytes.Buffer
	code := cli.RunWithHTTP(context.Background(), append([]string{"--server", "http://metronome.test", "--as", as}, args...), &out, &errOut, e.http)
	return out.String(), errOut.String(), code
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

func (e *env) approvedItem(item flow.BacklogItem, slices []flow.SliceSpec) {
	e.t.Helper()
	e.ok("pat", "backlog", "sync", e.file("backlog.json", api.BacklogRequest{Items: []flow.BacklogItem{item}}))
	e.ok("agent", "plan", "start")
	e.ok("agent", "plan", "propose", "I1", "--slices", e.file("slices.json", slices))
	e.ok("pat", "plan", "approve", "I1")
}

func newBriefEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.approvedItem(flow.BacklogItem{
		Ref: "ACME-1", Title: "Add export", Intent: "Let customers download their data\nInclude all records", Owner: "pat", PredictedTier: 1,
	}, []flow.SliceSpec{
		{Title: "API endpoint", Repo: "acme/app", Criteria: []string{"GET /export returns CSV", "An empty account exports a header row\nNo data rows"}, Approvers: []string{"rev", "sam"}},
		{Title: "Docs", Repo: "acme/docs"},
	})
	return e
}

func TestNextPrintsTheSliceBrief(t *testing.T) {
	e := newBriefEnv(t)
	out := e.ok("laptop", "next", "--runner", "laptop")
	if !strings.HasPrefix(out, "run R1 claimed slice I1.1; lease until ") {
		t.Fatalf("claim line:\n%s", out)
	}
	for _, want := range []string{
		"slice:     I1.1 API endpoint\n",
		"repo:      acme/app\n",
		"item:      ACME-1 Add export (I1)\n",
		"intent:    Let customers download their data\n           Include all records\n",
		"criteria:\n  - GET /export returns CSV\n  - An empty account exports a header row\n    No data rows\n",
		"approvers: rev, sam\n",
		"resume:    no earlier run\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("next missing %q:\n%s", want, out)
		}
	}
	for _, label := range []string{"rework:", "previous:", "asks:"} {
		if strings.Contains(out, label) {
			t.Fatalf("fresh claim contains %q:\n%s", label, out)
		}
	}
	out = e.ok("laptop", "next", "--runner", "laptop", "--slice", "I1.2")
	if !strings.Contains(out, "slice:     I1.2 Docs\nrepo:      acme/docs\n") || !strings.Contains(out, "criteria:  none given\n") || strings.Contains(out, "approvers:") {
		t.Fatalf("next without criteria or approvers:\n%s", out)
	}
}

func decodeBrief(t *testing.T, out string) map[string]map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(out))
	var sections map[string]json.RawMessage
	if err := decoder.Decode(&sections); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatalf("expected one JSON object, got %v", err)
	}
	brief := make(map[string]map[string]any)
	for key, raw := range sections {
		var section map[string]any
		if err := json.Unmarshal(raw, &section); err != nil {
			t.Fatal(err)
		}
		brief[key] = section
	}
	return brief
}

func TestNextJSONPrintsTheBrief(t *testing.T) {
	e := newBriefEnv(t)
	start := time.Now()
	out := e.ok("laptop", "next", "--runner", "laptop", "--json")
	brief := decodeBrief(t, out)
	lease, ok := brief["claim"]["lease_until"].(string)
	if !ok {
		t.Fatalf("missing lease: %v", brief)
	}
	until, err := time.Parse(time.RFC3339Nano, lease)
	if err != nil || !strings.HasSuffix(lease, "Z") || !until.After(start) {
		t.Fatalf("lease_until = %q, want a future UTC timestamp", lease)
	}
	want := map[string]map[string]any{
		"claim":   {"run_id": "R1", "slice_id": "I1.1", "lease_until": lease},
		"slice":   {"id": "I1.1", "repo": "acme/app", "title": "API endpoint", "criteria": []any{"GET /export returns CSV", "An empty account exports a header row\nNo data rows"}, "approvers": []any{"rev", "sam"}},
		"item":    {"id": "I1", "ref": "ACME-1", "title": "Add export", "intent": "Let customers download their data\nInclude all records"},
		"history": emptyHistory(),
	}
	if !reflect.DeepEqual(brief, want) || !strings.Contains(out, `"asks": []`) || !strings.Contains(out, `"pr": null`) {
		t.Fatalf("brief = %#v, want %#v", brief, want)
	}
	out = e.ok("laptop", "next", "--runner", "laptop", "--slice", "I1.2", "--json")
	brief = decodeBrief(t, out)
	want["claim"] = map[string]any{"run_id": "R2", "slice_id": "I1.2", "lease_until": brief["claim"]["lease_until"]}
	want["slice"] = map[string]any{"id": "I1.2", "repo": "acme/docs", "title": "Docs", "criteria": []any{}, "approvers": []any{}}
	if !reflect.DeepEqual(brief, want) || !strings.Contains(out, `"approvers": []`) {
		t.Fatalf("second brief = %s, want %#v", out, want)
	}
}

func emptyHistory() map[string]any {
	return map[string]any{"reason": "", "rework": false, "rework_reason": "", "failed_head": "", "changes_requested_by": "", "previous_run": "", "branch": "", "pr": nil, "asks": []any{}}
}

func (e *env) resumedBrief(requestID string, wantText ...string) map[string]any {
	e.t.Helper()
	args := []string{"--request-id", requestID, "next", "--runner", "cloud"}
	text := e.ok("cloud", args...)
	for _, want := range wantText {
		if !strings.Contains(text, want) {
			e.t.Errorf("next missing %q:\n%s", want, text)
		}
	}
	return decodeBrief(e.t, e.ok("cloud", append(args, "--json")...))["history"]
}

func TestNextResumesAnsweredAsk(t *testing.T) {
	e := newBriefEnv(t)
	e.ok("laptop", "next", "--runner", "laptop")
	e.ok("laptop", "ask", "--run", "R1", "--type", "answer", "--question", "CSV or JSON?")
	start := time.Now()
	e.ok("pat", "answer", "F2", "CSV\nwith headers")
	history := e.resumedBrief("resume", "resume:    asks answered after run R1\n", "previous:  run R1\n",
		"asks:\n  - F2 answer: CSV or JSON?\n      answer: CSV\n              with headers\n      answered by pat at ")
	asks, ok := history["asks"].([]any)
	if !ok || len(asks) != 1 {
		t.Fatalf("asks = %#v", history["asks"])
	}
	ask, ok := asks[0].(map[string]any)
	if !ok {
		t.Fatalf("ask = %#v", asks[0])
	}
	at, ok := ask["answered_at"].(string)
	when, err := time.Parse(time.RFC3339Nano, at)
	if !ok || err != nil || !strings.HasSuffix(at, "Z") || when.Before(start) || when.After(time.Now()) {
		t.Fatalf("answered_at = %q", at)
	}
	want := emptyHistory()
	want["reason"], want["previous_run"] = "answered_ask", "R1"
	want["asks"] = []any{map[string]any{"id": "F2", "type": "answer", "question": "CSV or JSON?", "answer": "CSV\nwith headers", "answered_by": "pat", "answered_at": at}}
	if !reflect.DeepEqual(history, want) {
		t.Fatalf("history = %#v, want %#v", history, want)
	}
}

func (e *env) submittedBrief() map[string]any {
	e.t.Helper()
	e.ok("laptop", "next", "--runner", "laptop")
	e.ok("laptop", "submit", "R1", "--branch", "metronome/I1.1")
	e.ok("github", "github", "pr-opened", "--slice", "I1.1", "--repo", "acme/app", "--number", "7", "--head", "abc123")
	want := emptyHistory()
	want["previous_run"], want["branch"], want["rework"] = "R1", "metronome/I1.1", true
	want["pr"] = map[string]any{"repo": "acme/app", "number": float64(7), "head_sha": "abc123"}
	return want
}

func TestNextReworkAfterFailedChecks(t *testing.T) {
	e := newBriefEnv(t)
	want := e.submittedBrief()
	e.ok("github", "github", "checks", "--slice", "I1.1", "--head", "abc123", "--failure")
	want["reason"], want["rework_reason"], want["failed_head"] = "checks_failed", "checks_failed", "abc123"
	history := e.resumedBrief("resume", "resume:    checks failed on abc123\n", "rework:    checks failed on abc123\n", "previous:  run R1, branch metronome/I1.1, PR acme/app#7 at abc123\n")
	if !reflect.DeepEqual(history, want) {
		t.Fatalf("history = %#v, want %#v", history, want)
	}
	e.ok("cloud", "drop", "R2")
	want["reason"], want["previous_run"] = "dropped", "R2"
	history = e.resumedBrief("after-drop", "resume:    run R2 was dropped\n", "rework:    checks failed on abc123\n")
	if !reflect.DeepEqual(history, want) {
		t.Fatalf("history = %#v, want %#v", history, want)
	}
}

func TestNextReworkAfterChangeRequest(t *testing.T) {
	e := newBriefEnv(t)
	want := e.submittedBrief()
	e.ok("github", "github", "checks", "--slice", "I1.1", "--head", "abc123", "--success")
	e.ok("github", "github", "ready-for-review", "--slice", "I1.1")
	e.ok("github", "github", "changes-requested", "--slice", "I1.1", "--by", "rev")
	want["reason"], want["rework_reason"], want["changes_requested_by"] = "changes_requested", "changes_requested", "rev"
	history := e.resumedBrief("resume", "resume:    changes requested by rev\n", "rework:    changes requested by rev\n")
	if !reflect.DeepEqual(history, want) {
		t.Fatalf("history = %#v, want %#v", history, want)
	}
	e.ok("cloud", "submit", "R2", "--branch", "metronome/I1.1")
	out := e.ok("pat", "board", "--json")
	if e.board().Slices[0].Rework || strings.Contains(out, `"rework_reason"`) {
		t.Fatalf("rework not cleared on submit: %s", out)
	}
}

func TestNextWithoutIntent(t *testing.T) {
	e := newEnv(t)
	e.approvedItem(flow.BacklogItem{Ref: "ACME-1", Title: "Add export", Owner: "pat"}, []flow.SliceSpec{{Title: "Docs", Repo: "acme/docs"}})
	args := []string{"--request-id", "q1", "next", "--runner", "laptop"}
	if out := e.ok("laptop", args...); !strings.Contains(out, "intent:    none given\n") {
		t.Fatalf("next without intent:\n%s", out)
	}
	brief := decodeBrief(t, e.ok("laptop", append(args, "--json")...))
	if brief["item"]["intent"] != "" {
		t.Fatalf("intent = %#v, want empty string", brief["item"]["intent"])
	}
}

func TestNextFailsWithoutPartialBrief(t *testing.T) {
	for _, tc := range []struct {
		name   string
		path   string
		status int
		body   string
		want   string
	}{
		{"board read fails", "/v1/board", http.StatusServiceUnavailable, "unavailable", "unavailable"},
		{"slice missing", "/v1/board", http.StatusOK, `{}`, "slice I1.1 missing from board"},
		{"item missing", "/v1/board", http.StatusOK, `{"slices":[{"id":"I1.1","item_id":"I1"}]}`, "item I1 missing from board"},
		{"history read fails", "/v1/runs/R1/history", http.StatusServiceUnavailable, "unavailable", "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBriefEnv(t)
			handler := e.http.Transport.(handlerTransport).h
			e.http.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == tc.path {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, tc.body)
					return
				}
				handler.ServeHTTP(w, r)
			})}
			args := []string{"--request-id", "q1", "next", "--runner", "laptop"}
			for _, flags := range [][]string{args, append(args, "--json")} {
				out, errOut, code := e.runOutput("laptop", flags...)
				if code != 1 || out != "" || !strings.Contains(errOut, "run R1 claimed slice I1.1, but its brief could not be read:") || !strings.Contains(errOut, tc.want) {
					t.Fatalf("next exited %d, stdout %q, stderr %q", code, out, errOut)
				}
			}
		})
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
	e.ok("pat", "plan", "approve", "I1", "--tier", "1")

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
	first = e.ok("laptop", "--request-id", "q2", "next", "--runner", "laptop", "--json")
	again = e.ok("laptop", "--request-id", "q2", "next", "--runner", "laptop", "--json")
	if first != again {
		t.Fatalf("repeated JSON claim:\n%s\nvs\n%s", first, again)
	}
	if u := e.board().Usage; u.Execute != 2 {
		t.Fatalf("execute usage = %d, want 2", u.Execute)
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
