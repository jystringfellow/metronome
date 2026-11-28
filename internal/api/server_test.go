package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/cli"
	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/store"
)

func testServer(t *testing.T, storage flow.Store) *httptest.Server {
	t.Helper()
	eng, err := flow.NewEngine(context.Background(), flow.DefaultConfig(), storage, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.Handler(eng))
	t.Cleanup(server.Close)
	return server
}

func request(t *testing.T, server *httptest.Server, method, path string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func TestUnknownRunHistory(t *testing.T) {
	server := testServer(t, store.NewMemory())
	resp, body := request(t, server, http.MethodGet, "/v1/runs/R9/history")
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), `"code":"not_found"`) {
		t.Fatalf("history returned %d: %s", resp.StatusCode, body)
	}
}

func TestAdoptionAPI(t *testing.T) {
	server := testServer(t, store.NewMemory())
	client := api.Client{BaseURL: server.URL, HTTP: server.Client(), Actor: "pat"}
	err := client.Post(t.Context(), "/v1/github/adopt", "", map[string]any{"unknown": true}, nil)
	var status *api.StatusError
	if !errors.As(err, &status) || status.Code != http.StatusBadRequest || !strings.Contains(err.Error(), "unknown field") {
		t.Fatal(err)
	}
	var result flow.Result
	if err := client.Post(t.Context(), "/v1/workstreams/default/adopt-label", "", api.AdoptLabelRequest{Label: "adopt"}, &result); err != nil || len(result.Events) != 1 || result.Events[0].Type != flow.EvWorkstreamAdoptLabelSet {
		t.Fatal(result, err)
	}
	if err := client.Post(t.Context(), "/v1/github/adopt", "", api.AdoptRequest{Repo: "acme/app", Number: 12, HeadRepo: "acme/app", Branch: "feature-x", HeadSHA: "abc", Author: "pat", Title: "Export"}, &result); err != nil || len(result.Events) != 1 || result.Events[0].Type != flow.EvPRAdopted {
		t.Fatal(result, err)
	}
	var board api.Board
	if err := client.Get(t.Context(), "/v1/board", &board); err != nil || board.Workstreams[0].AdoptLabel != "adopt" || board.Slices[0].Adopted.Number != 12 {
		t.Fatal(board, err)
	}
}

func TestRunLookup(t *testing.T) {
	ctx := context.Background()
	eng, err := flow.NewEngine(ctx, flow.DefaultConfig(), store.NewMemory(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = eng.SyncBacklog(ctx, "pat", "", "", []flow.BacklogItem{{Ref: "ACME-1", Title: "Export reports", Owner: "pat"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = eng.StartPlan(ctx, "pat", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = eng.ProposePlan(ctx, "pat", "", "I1", []flow.SliceSpec{{Title: "CSV export", Repo: "acme/app"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	tier := 1
	_, err = eng.ApprovePlan(ctx, "pat", "", "I1", &tier)
	if err != nil {
		t.Fatal(err)
	}
	_, err = eng.Claim(ctx, "laptop", "", "laptop", "", "")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.Handler(eng))
	defer server.Close()
	client := api.Client{BaseURL: server.URL, HTTP: server.Client()}
	var open flow.Run
	if err := client.Get(ctx, "/v1/runs/R1", &open); err != nil || open.ID != "R1" || open.Closed || open.SliceID != "I1.1" || open.LeaseUntil.IsZero() {
		t.Fatalf("open run %+v: %v", open, err)
	}
	if _, err := eng.ReleaseRun(ctx, "laptop", "", "R1"); err != nil {
		t.Fatal(err)
	}
	var closed flow.Run
	if err := client.Get(ctx, "/v1/runs/R1", &closed); err != nil || !closed.Closed || closed.ClosedReason != flow.CloseReleased || !closed.LeaseUntil.Equal(open.LeaseUntil) {
		t.Fatalf("closed run %+v: %v", closed, err)
	}
	resp, body := request(t, server, http.MethodGet, "/v1/runs/R9")
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), `"code":"not_found"`) {
		t.Fatalf("unknown run returned %d: %s", resp.StatusCode, body)
	}
}

func TestClientStatusError(t *testing.T) {
	for _, tc := range []struct {
		status        int
		body, message string
		code          flow.ErrorCode
	}{
		{503, "unavailable", "unavailable", ""},
		{500, `{"code":"internal","message":"unavailable"}`, "internal: unavailable", "internal"},
		{409, `{"code":"conflict","message":"run is closed"}`, "conflict: run is closed", flow.CodeConflict},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client := api.Client{BaseURL: server.URL, HTTP: server.Client()}
			err := client.Post(context.Background(), "/v1/runs/R1/submit", "", nil, nil)
			var status *api.StatusError
			if !errors.As(err, &status) || status.Code != tc.status || flow.CodeOf(err) != tc.code || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("status error %v (%+v), want %d, %s", err, status, tc.status, tc.code)
			}
		})
	}
}

func TestResumedBriefOverHTTP(t *testing.T) {
	dir := t.TempDir()
	storage, err := store.OpenSQLite(context.Background(), filepath.Join(dir, "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	server := testServer(t, storage)
	run := func(actor string, args ...string) string {
		t.Helper()
		var output, stderr bytes.Buffer
		code := cli.RunWithHTTP(context.Background(), append([]string{"--server", server.URL, "--as", actor}, args...), &output, &stderr, server.Client())
		if code != 0 {
			t.Fatalf("%v: exit %d: %s%s", args, code, output.String(), stderr.String())
		}
		return output.String()
	}
	backlog := filepath.Join(dir, "backlog.json")
	slices := filepath.Join(dir, "slices.json")
	if err := os.WriteFile(backlog, []byte(`{"items":[{"ref":"ACME-1","title":"Export reports","owner":"pat","predicted_tier":1}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(slices, []byte(`[{"title":"CSV export","repo":"acme/app"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	run("pat", "backlog", "sync", backlog)
	run("pat", "plan", "start")
	run("pat", "plan", "propose", "I1", "--slices", slices)
	run("pat", "plan", "approve", "I1")
	run("laptop", "next", "--runner", "laptop")
	run("laptop", "ask", "--run", "R1", "--type", "answer", "--question", "CSV or JSON?")
	run("pat", "answer", "F2", "CSV")
	first := run("cloud", "--request-id", "resume", "next", "--runner", "cloud", "--json")
	var brief struct {
		History flow.SliceHistory `json:"history"`
	}
	if err := json.Unmarshal([]byte(first), &brief); err != nil {
		t.Fatal(err)
	}
	if history := brief.History; history.Reason != flow.ResumeAnswered || history.PreviousRun != "R1" || len(history.Asks) != 1 || history.Asks[0].Answer != "CSV" {
		t.Fatalf("unexpected resume brief: %s", first)
	}
	wrapped := run("cloud", "--request-id", "resume", "run", "--runner", "cloud", "--", "sh", "-c", `cat "$METRONOME_BRIEF"`)
	if wrapped != first {
		t.Fatalf("wrapped brief = %s, want %s", wrapped, first)
	}
}

func TestBoardAssets(t *testing.T) {
	server := testServer(t, store.NewMemory())
	assets := map[string]string{}
	for _, asset := range []struct{ path, file, contentType string }{
		{"/", "index.html", "text/html; charset=utf-8"},
		{"/board.js", "board.js", "text/javascript; charset=utf-8"},
		{"/board.css", "board.css", "text/css; charset=utf-8"},
	} {
		resp, body := request(t, server, http.MethodGet, asset.path)
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != asset.contentType || resp.Header.Get("Cache-Control") != "no-cache" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: %d %v", asset.path, resp.StatusCode, resp.Header)
		}
		want, err := os.ReadFile(filepath.Join("web", asset.file))
		if err != nil {
			t.Fatal(err)
		}
		if len(body) == 0 || !bytes.Equal(body, want) {
			t.Fatalf("%s: asset does not match source", asset.path)
		}
		assets[asset.path] = string(body)
		if asset.path == "/" {
			csp := resp.Header.Get("Content-Security-Policy")
			for _, directive := range []string{"default-src 'none'", "form-action 'none'", "script-src 'self'", "style-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
				if !strings.Contains(csp, directive) {
					t.Errorf("CSP missing %q: %s", directive, csp)
				}
			}
		}
	}
	html := assets["/"]
	for _, text := range []string{"<title>Metronome</title>", "/board.js", "/board.css", "Workstreams", "Labels", "Needs attention", "Recent activity"} {
		if !strings.Contains(html, text) {
			t.Errorf("page missing %q", text)
		}
	}
	for _, text := range []string{"<form", "<button", "<input", "<select", "<textarea", "://", "<style"} {
		if strings.Contains(strings.ToLower(html), text) {
			t.Errorf("page contains %q", text)
		}
	}
	if regexp.MustCompile(`(?i)\son[a-z]+\s*=`).MatchString(html) || regexp.MustCompile(`(?is)<script[^>]*>\s*[^\s<]`).MatchString(html) {
		t.Error("page contains inline script")
	}
	for _, text := range []string{"url(", "@import"} {
		if strings.Contains(assets["/board.css"], text) {
			t.Errorf("CSS contains %q", text)
		}
	}
	for _, text := range []string{"prefers-color-scheme: dark", "prefers-reduced-motion", ":focus-visible", "max-width"} {
		if !strings.Contains(assets["/board.css"], text) {
			t.Errorf("CSS missing %q", text)
		}
	}
	script := assets["/board.js"]
	if !strings.Contains(script, "item.labels") {
		t.Error("board script missing item labels")
	}
	for _, text := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function", "method:", "POST", "XMLHttpRequest", "WebSocket", "setAttribute('style'", `setAttribute("style"`, ".style."} {
		if strings.Contains(script, text) {
			t.Errorf("script contains %q", text)
		}
	}
	if strings.Contains(strings.ReplaceAll(script, "https://github.com/", ""), "://") {
		t.Error("script contains an unexpected absolute URL")
	}
	for _, path := range []string{"/v1/board", "/v1/events", "pushState", "popstate", "URLSearchParams", "lease_seconds"} {
		if !strings.Contains(script, path) {
			t.Errorf("script missing %s", path)
		}
	}
	resp, body := request(t, server, http.MethodHead, "/")
	if resp.StatusCode != 200 || len(body) != 0 {
		t.Fatalf("HEAD /: %d, %d bytes", resp.StatusCode, len(body))
	}
}

func TestBoardRoutesDoNotShadowAPI(t *testing.T) {
	server := testServer(t, store.NewMemory())
	for _, route := range []struct {
		method, path string
		status       int
		json         bool
	}{
		{"GET", "/nope", 404, false},
		{"GET", "/v1/nope", 404, false},
		{"POST", "/", 405, false},
		{"GET", "/v1/board", 200, true},
		{"GET", "/v1/inbox/pat", 200, true},
		{"GET", "/v1/events", 200, true},
		{"POST", "/v1/tick", 200, true},
		{"POST", "/v1/backlog/sync", 400, true},
		{"POST", "/v1/plan/start", 400, true},
		{"POST", "/v1/items/I1/plan/propose", 400, true},
		{"POST", "/v1/items/I1/plan/approve", 400, true},
		{"POST", "/v1/items/I1/plan/drop", 400, true},
		{"POST", "/v1/items/I1/release-done", 400, true},
		{"POST", "/v1/flags", 400, true},
		{"POST", "/v1/flags/F1/resolve", 400, true},
		{"POST", "/v1/claims", 400, true},
		{"POST", "/v1/runs/R1/heartbeat", 400, true},
		{"POST", "/v1/runs/R1/flag", 400, true},
		{"POST", "/v1/runs/R1/submit", 400, true},
		{"POST", "/v1/runs/R1/release", 400, true},
		{"POST", "/v1/github/pr-opened", 400, true},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			resp, body := request(t, server, route.method, route.path)
			if resp.StatusCode != route.status || (route.json && resp.Header.Get("Content-Type") != "application/json") {
				t.Fatalf("got %d %s: %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
			}
			if route.status == 400 {
				var failure flow.Error
				if err := json.Unmarshal(body, &failure); err != nil || failure.Code != flow.CodeInvalid {
					t.Fatalf("expected invalid error, got %s", body)
				}
			}
		})
	}
}

func TestBoardData(t *testing.T) {
	dir := t.TempDir()
	storage, err := store.OpenSQLite(context.Background(), filepath.Join(dir, "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	server := testServer(t, storage)
	run := func(actor string, args ...string) {
		t.Helper()
		var output bytes.Buffer
		code := cli.RunWithHTTP(context.Background(), append([]string{"--server", server.URL, "--as", actor}, args...), &output, &output, server.Client())
		if code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, output.String())
		}
	}
	backlog := filepath.Join(dir, "backlog.json")
	slices := filepath.Join(dir, "slices.json")
	if err := os.WriteFile(backlog, []byte(`{"items":[{"ref":"ACME-1","title":"Export reports","owner":"pat","predicted_tier":1,"labels":[" Slow ","business"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(slices, []byte(`[{"title":"CSV export","repo":"acme/app","criteria":["exports CSV"],"approvers":["rev"]}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	run("pat", "backlog", "sync", backlog)
	run("pat", "plan", "start")
	run("pat", "plan", "propose", "I1", "--slices", slices)
	run("pat", "plan", "approve", "I1")
	run("laptop", "next", "--runner", "laptop")
	run("pat", "ask", "--item", "I1", "--type", "answer", "--question", "CSV or JSON?")
	run("pat", "github", "pr-opened", "--slice", "I1.1", "--repo", "acme/app", "--number", "7", "--head", "abcdef0")

	page, _ := request(t, server, http.MethodGet, "/")
	if page.StatusCode != 200 || page.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("populated board page: %d %v", page.StatusCode, page.Header)
	}
	resp, body := request(t, server, http.MethodGet, "/v1/board")
	if _, err := http.ParseTime(resp.Header.Get("Date")); err != nil {
		t.Fatalf("board response lacks server time: %v", err)
	}
	var board api.Board
	if err := json.Unmarshal(body, &board); err != nil || resp.StatusCode != 200 {
		t.Fatalf("board: %d %s: %v", resp.StatusCode, body, err)
	}
	if len(board.Items) != 1 || board.Items[0].Title != "Export reports" || len(board.Slices) != 1 || board.Slices[0].PR == nil || board.Slices[0].PR.Number != 7 || board.Slices[0].PR.Repo != "acme/app" {
		t.Fatalf("unexpected items or slices: %s", body)
	}
	if len(board.Runs) != 1 || board.Runs[0].RunnerID != "laptop" || !board.Runs[0].LeaseUntil.After(time.Now()) || len(board.Flags) != 1 || board.Flags[0].Owner != "pat" || board.Flags[0].Question != "CSV or JSON?" {
		t.Fatalf("unexpected runs or flags: %s", body)
	}
	if board.Limits != flow.DefaultConfig().Limits || board.Usage.Execute != 1 || board.Usage.Review != 1 || board.Usage.PerApprover == nil {
		t.Fatalf("unexpected limits or usage: %s", body)
	}
	if board.LeaseSeconds != int64(flow.DefaultConfig().Lease/time.Second) {
		t.Fatalf("unexpected lease seconds: %d", board.LeaseSeconds)
	}
	if !reflect.DeepEqual(board.Items[0].Labels, []string{"business", "slow"}) {
		t.Fatal(board.Items[0].Labels)
	}
	for _, query := range []string{"label=slow", "label=SLOW", "label=slow&workstream=default"} {
		resp, body := request(t, server, http.MethodGet, "/v1/board?"+query)
		var filtered api.Board
		if err := json.Unmarshal(body, &filtered); err != nil || resp.StatusCode != 200 || !reflect.DeepEqual(board, filtered) {
			t.Fatalf("%s: %d %s: %v", query, resp.StatusCode, body, err)
		}
	}
	resp, body = request(t, server, http.MethodGet, "/v1/board?label=missing")
	var filtered api.Board
	if err := json.Unmarshal(body, &filtered); err != nil || resp.StatusCode != 200 ||
		!reflect.DeepEqual(filtered.Items, []flow.Item{}) || !reflect.DeepEqual(filtered.Slices, []flow.Slice{}) ||
		!reflect.DeepEqual(filtered.Runs, []flow.Run{}) || !reflect.DeepEqual(filtered.Flags, []flow.Flag{}) ||
		!reflect.DeepEqual(board.Usage, filtered.Usage) || !reflect.DeepEqual(board.Workstreams, filtered.Workstreams) || board.Limits != filtered.Limits || board.LeaseSeconds != filtered.LeaseSeconds {
		t.Fatalf("nonmatching label: %d %s: %v", resp.StatusCode, body, err)
	}
	for _, query := range []string{"label=a,b", "label="} {
		resp, body := request(t, server, http.MethodGet, "/v1/board?"+query)
		if resp.StatusCode != 400 || !strings.Contains(string(body), `"code":"invalid"`) {
			t.Fatalf("invalid label: %d %s", resp.StatusCode, body)
		}
	}
	run("pat", "workstream", "create", "example", "--execute", "2")
	if err := os.WriteFile(backlog, []byte(`{"items":[{"ref":"ACME-2","title":"Import reports","owner":"pat"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	run("pat", "backlog", "sync", "--workstream", "example", backlog)
	resp, body = request(t, server, http.MethodGet, "/v1/board")
	var grouped api.Board
	if err := json.Unmarshal(body, &grouped); err != nil || resp.StatusCode != 200 || len(grouped.Workstreams) != 2 || len(grouped.Items) != 2 {
		t.Fatalf("grouped board: %d %s: %v", resp.StatusCode, body, err)
	}
	defaultView, exampleView := grouped.Workstreams[0], grouped.Workstreams[1]
	if defaultView.Name != "default" || defaultView.Limits.Execute != 5 || defaultView.Usage.Execute != 1 || exampleView.Name != "example" || exampleView.Limits.Execute != 2 || exampleView.Usage.Execute != 0 || exampleView.Usage.Ready != 1 {
		t.Fatalf("unexpected workstreams: %+v", grouped.Workstreams)
	}
	items, sliceStreams := map[string]string{}, map[string]string{}
	for _, item := range grouped.Items {
		items[item.ID] = item.Workstream
	}
	for _, slice := range grouped.Slices {
		sliceStreams[slice.ID] = slice.Workstream
		if slice.Workstream == "" || items[slice.ItemID] != slice.Workstream {
			t.Fatalf("slice without matching item workstream: %+v", slice)
		}
	}
	for _, activeRun := range grouped.Runs {
		if sliceStreams[activeRun.SliceID] == "" {
			t.Fatalf("run without slice workstream: %+v", activeRun)
		}
	}
	for _, flag := range grouped.Flags {
		if flag.Subject.Kind == flow.SubjectItem && items[flag.Subject.ID] == "" || flag.Subject.Kind == flow.SubjectSlice && sliceStreams[flag.Subject.ID] == "" {
			t.Fatalf("flag without subject workstream: %+v", flag)
		}
	}
	resp, body = request(t, server, http.MethodGet, "/v1/board?workstream=example")
	var example api.Board
	if err := json.Unmarshal(body, &example); err != nil || resp.StatusCode != 200 || example.LeaseSeconds != board.LeaseSeconds || !reflect.DeepEqual(example.Workstreams, []flow.WorkstreamView{exampleView}) || len(example.Items) != 1 || example.Items[0].Workstream != "example" || len(example.Runs) != 0 || len(example.Flags) != 0 {
		t.Fatalf("scoped board: %d %s: %v", resp.StatusCode, body, err)
	}
	readEvents := func(path string) []flow.Event {
		t.Helper()
		resp, body := request(t, server, http.MethodGet, path)
		var events []flow.Event
		if err := json.Unmarshal(body, &events); err != nil || resp.StatusCode != 200 || events == nil {
			t.Fatalf("events: %d %s: %v", resp.StatusCode, body, err)
		}
		return events
	}
	first := readEvents("/v1/events?after=0&limit=2")
	all := readEvents("/v1/events?after=0")
	if len(first) != 2 || len(all) <= 2 || first[0].Seq != all[0].Seq || first[1].Seq != all[1].Seq {
		t.Fatalf("unexpected event pages: %v / %v", first, all)
	}
	for index := 1; index < len(all); index++ {
		if all[index].Seq <= all[index-1].Seq {
			t.Fatal("events out of order")
		}
	}
	if remaining := readEvents("/v1/events?after=" + strconv.FormatInt(all[len(all)-1].Seq, 10)); len(remaining) != 0 {
		t.Fatalf("expected empty tail, got %v", remaining)
	}
	t.Logf("page: %d %s; execute: %d/%d; review: %d/%d; open runs: %d; open asks: %d; events: %d", page.StatusCode, page.Header.Get("Content-Type"), board.Usage.Execute, board.Limits.Execute, board.Usage.Review, board.Limits.Review, len(board.Runs), len(board.Flags), len(all))
}
