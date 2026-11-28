package api_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/store"
)

func TestBoardPage(t *testing.T) {
	ctx := t.Context()
	now := time.Now()
	eng, err := flow.NewEngine(ctx, flow.DefaultConfig(), store.NewMemory(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ok := func(_ flow.Result, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	readyLimit := 35
	ok(eng.CreateWorkstream(ctx, "pat", "", "example", flow.PartialLimits{Ready: &readyLimit}))
	ok(eng.CreateWorkstream(ctx, "pat", "", "team", flow.PartialLimits{}))
	longLabel := strings.Repeat("a", 40)
	items := []flow.BacklogItem{
		{Ref: "ACME-1", Title: "Report", Owner: "pat", Labels: []string{"Slow", "business"}, Slices: []flow.SliceSpec{{Title: "Running report slice", Repo: "acme/app"}}},
		{Ref: "ACME-2", Title: "Export", Owner: "pat", Labels: []string{"business"}, Slices: []flow.SliceSpec{{Title: "Review export slice", Repo: "acme/app", Approvers: []string{"sam"}}}},
	}
	for number := 3; number <= 35; number++ {
		items = append(items, flow.BacklogItem{Ref: fmt.Sprintf("ACME-%d", number), Title: fmt.Sprintf("Ready item %d", number), Owner: "pat", Labels: []string{longLabel}})
	}
	items[2].Labels = []string{"business"}
	ok(eng.SyncBacklog(ctx, "pat", "", "example", items))
	for _, id := range []string{"I1", "I2"} {
		ok(eng.StartPlan(ctx, "pat", "", "example", id))
		ok(eng.ApprovePlan(ctx, "pat", "", id, nil))
	}
	items = items[:32]
	ok(eng.IntakeBacklog(ctx, "pat", "", "example", items))
	ok(eng.Claim(ctx, "pat", "", "laptop", "example", "I1.1"))
	ok(eng.Claim(ctx, "pat", "", "laptop", "example", "I2.1"))
	ok(eng.Submit(ctx, "pat", "", "R2", "export"))
	ok(eng.PROpened(ctx, "pat", "", "I2.1", "acme/app", 7, "abcdef0"))
	ok(eng.ChecksCompleted(ctx, "pat", "", "I2.1", "abcdef0", true))
	ok(eng.ReadyForReview(ctx, "pat", "", "I2.1"))
	ok(eng.RaiseFlag(ctx, "pat", "", flow.Subject{Kind: flow.SubjectItem, ID: "I1"}, flow.AskAnswer, "Which format?", "pat"))
	ok(eng.SyncBacklog(ctx, "pat", "", "team", []flow.BacklogItem{{Ref: "TEAM-1", Title: "Team-only work", Owner: "sam", Slices: []flow.SliceSpec{{Title: "Team-only slice", Repo: "acme/app"}}}}))
	ok(eng.StartPlan(ctx, "sam", "", "team", "I36"))
	ok(eng.ApprovePlan(ctx, "sam", "", "I36", nil))
	ok(eng.SyncBacklog(ctx, "pat", "", "default", []flow.BacklogItem{
		{Ref: "DEFAULT-1", Title: "Default Ready one", Owner: "pat"},
		{Ref: "DEFAULT-2", Title: "Default Ready two", Owner: "pat"},
		{Ref: "DEFAULT-3", Title: "Default Ready three", Owner: "pat"},
	}))
	now = now.Add(-20 * time.Minute)
	ok(eng.Claim(ctx, "sam", "", "team-runner", "team", "I36.1"))
	eng.View(func(state *flow.State) {
		if state.StageUsage("default").Ready != 3 || state.StageUsage("example").Ready != 30 || state.StageUsage("example").Execute != 1 || state.StageUsage("team").Execute != 1 {
			t.Fatalf("unexpected fixture usage: %+v", state.WorkstreamViews())
		}
	})
	browser := os.Getenv("METRONOME_BROWSER")
	if browser == "" {
		t.Skip("set METRONOME_BROWSER to a Chromium executable to test live board rendering")
	}
	before := eng.EventsAfter(0, 0)
	handler := api.Handler(eng)
	var mu sync.Mutex
	var change bool
	var pages, reads int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method != http.MethodGet {
			t.Errorf("board made a %s request to %s", r.Method, r.URL.Path)
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		handler.ServeHTTP(w, r)
		if r.URL.Path == "/" {
			pages++
		}
		if r.URL.Path == "/v1/board" {
			reads++
			if change && reads == 1 {
				items[2].Labels = []string{" Report "}
				if result, err := eng.IntakeBacklog(ctx, "pat", "", "example", items); err != nil || len(result.Events) != 1 {
					t.Errorf("label update: %+v, %v", result, err)
				}
			}
		}
	}))
	defer server.Close()
	render := func(path, size string) string {
		t.Helper()
		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, browser, "--headless", "--disable-background-networking",
			"--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1, EXCLUDE localhost", "--user-data-dir="+t.TempDir(),
			"--window-size="+size, "--enable-logging=stderr", "--dump-dom", "--timeout=15000", "--virtual-time-budget=8000", server.URL+path)
		cmd.Stderr = &stderr
		body, err := cmd.Output()
		if err != nil {
			t.Fatalf("browser: %v\n%s", err, stderr.String())
		}
		if strings.Contains(stderr.String(), "ERROR:CONSOLE") {
			t.Fatal(stderr.String())
		}
		dom := string(body)
		if !strings.Contains(dom, "Last updated") || strings.Contains(dom, "Not updated since") {
			t.Fatal(dom)
		}
		if regexp.MustCompile(`(?i)<(form|button|input|select|textarea)\b`).MatchString(dom) {
			t.Fatal("rendered board has mutation controls")
		}
		return dom
	}
	assertContains := func(dom string, texts ...string) {
		t.Helper()
		for _, text := range texts {
			if !strings.Contains(dom, text) {
				t.Errorf("rendered board missing %q", text)
			}
		}
	}
	assertCurrent := func(dom, href string) {
		t.Helper()
		if !regexp.MustCompile(`<a[^>]*href="` + regexp.QuoteMeta(href) + `"[^>]*aria-current="page"`).MatchString(dom) {
			t.Errorf("no current link for %q", href)
		}
	}
	initial := render("/", "1200,900")
	assertContains(initial, `href="?workstream=example"`, `href="?workstream=team"`, `data-workstream="default"`, `data-workstream="example"`, `data-workstream="team"`,
		"Execute: 1 of 5 used", "1/5", "30/35", "Running report slice", "Review export slice", "Which format?", "25 more in Ready", "3 shelved", "lease-expired", "stale", longLabel, "claimed", "added", "Per approver (cap")
	assertCurrent(initial, "?")
	if !regexp.MustCompile(`(?s)<article class="card lease-expired" id="card-slice-I36\.1">.*?stale.*?</article>`).MatchString(initial) {
		t.Fatal("expired team run not marked stale")
	}
	assertLiveFirst := func(dom string, visibleRows int, liveCards ...string) {
		t.Helper()
		withoutDetails := regexp.MustCompile(`(?s)<details\b.*?</details>`).ReplaceAllString(dom, "")
		if count := strings.Count(withoutDetails, `class="card ready-row"`); count != visibleRows {
			t.Fatalf("expected %d visible Ready rows, got %d", visibleRows, count)
		}
		firstReady := strings.Index(withoutDetails, `class="card ready-row"`)
		firstSummary := strings.Index(dom, "more in Ready")
		for _, live := range liveCards {
			if index := strings.Index(withoutDetails, live); index < 0 || index >= firstReady {
				t.Errorf("live card %q did not appear before visible Ready rows", live)
			}
			if index := strings.Index(dom, live); firstSummary < 0 || index < 0 || index >= firstSummary {
				t.Errorf("live card %q did not appear before the Ready summary", live)
			}
		}
	}
	assertContains(initial, `class="backlog" data-workstream="default"`, `class="backlog" data-workstream="example"`, "Default Ready one")
	assertLiveFirst(initial, 8, `id="card-slice-I1.1"`, `id="card-slice-I2.1"`, `id="card-slice-I36.1"`)
	if strings.Contains(initial, ">item_added<") || strings.Contains(initial, `{"`) {
		t.Fatal("raw event data in rendered board")
	}
	scoped := render("/?workstream=example", "1200,900")
	assertCurrent(scoped, "?workstream=example")
	assertContains(scoped, `data-workstream="example"`, "Running report slice")
	assertLiveFirst(scoped, 5, `id="card-slice-I1.1"`, `id="card-slice-I2.1"`)
	if strings.Count(scoped, `class="lane"`) != 1 || strings.Contains(scoped, "Team-only") || strings.Contains(scoped, "Default Ready one") {
		t.Fatal("workstream filter leaked other lanes or activity")
	}
	labelled := render("/?label=business", "1200,900")
	assertCurrent(labelled, "?label=business")
	assertContains(labelled, `id="card-item-I3"`, "Running report slice", "Review export slice", "30/35", "1/5")
	if strings.Contains(labelled, `id="card-item-I4"`) || strings.Contains(labelled, "Team-only") {
		t.Fatal("label filter leaked other items or activity")
	}
	phone := render("/", "390,844")
	assertContains(phone, "Running report slice", "Recent activity", "30/35")
	assertLiveFirst(phone, 8, `id="card-slice-I1.1"`, `id="card-slice-I2.1"`, `id="card-slice-I36.1"`)
	if !reflect.DeepEqual(before, eng.EventsAfter(0, 0)) {
		t.Fatal("page reads changed log")
	}
	mu.Lock()
	change, pages, reads = true, 0, 0
	mu.Unlock()
	updated := render("/", "1200,900")
	assertContains(updated, `class="card ready-row changed" id="card-item-I3"`, `label=report`, "changes since")
	mu.Lock()
	if pages != 1 || reads < 2 {
		t.Errorf("expected a refresh without navigation: %d page loads, %d board reads", pages, reads)
	}
	mu.Unlock()
	after := eng.EventsAfter(0, 0)
	if len(after) != len(before)+1 || !reflect.DeepEqual(before, after[:len(before)]) || after[len(before)].Type != flow.EvItemUpdated {
		t.Fatal("unexpected events during page refresh")
	}
}
