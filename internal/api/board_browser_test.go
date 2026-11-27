package api_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/store"
)

func TestBoardPageLabels(t *testing.T) {
	browser := os.Getenv("METRONOME_BROWSER")
	if browser == "" {
		t.Skip("set METRONOME_BROWSER to a Chromium executable to test live board rendering")
	}
	eng, err := flow.NewEngine(t.Context(), flow.DefaultConfig(), store.NewMemory(), nil)
	if err != nil {
		t.Fatal(err)
	}
	longLabel := strings.Repeat("a", 40)
	items := []flow.BacklogItem{
		{Ref: "ACME-1", Title: "Report", Owner: "pat", Labels: []string{"Slow", "business"}, Slices: []flow.SliceSpec{{Title: "Report slice", Repo: "acme/app"}}},
		{Ref: "ACME-2", Title: "Export", Owner: "pat", Labels: []string{longLabel, "consumer"}},
		{Ref: "ACME-3", Title: "Import", Owner: "pat", Labels: []string{"reloads-often"}},
		{Ref: "ACME-4", Title: "Docs", Owner: "pat"},
	}
	if _, err := eng.SyncBacklog(t.Context(), "pat", "", "default", items); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.StartPlan(t.Context(), "pat", "", "default", "I1"); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ApprovePlan(t.Context(), "pat", "", "I1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.RaiseFlag(t.Context(), "pat", "", flow.Subject{Kind: flow.SubjectItem, ID: "I1"}, flow.AskAnswer, "Which format?", "pat"); err != nil {
		t.Fatal(err)
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
				items[1].Labels = []string{" Report "}
				items[2].Labels = nil
				if result, err := eng.IntakeBacklog(t.Context(), "pat", "", "default", items); err != nil || len(result.Events) != 2 {
					t.Errorf("label update: %+v, %v", result, err)
				}
			}
		}
	}))
	defer server.Close()
	render := func() string {
		t.Helper()
		var stderr bytes.Buffer
		cmd := exec.CommandContext(t.Context(), browser, "--headless", "--disable-background-networking",
			"--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1, EXCLUDE localhost", "--user-data-dir="+t.TempDir(),
			"--window-size=800,900", "--enable-logging=stderr", "--dump-dom", "--timeout=15000", "--virtual-time-budget=10000", server.URL)
		cmd.Stderr = &stderr
		body, err := cmd.Output()
		if err != nil {
			t.Fatalf("browser: %v\n%s", err, stderr.String())
		}
		if strings.Contains(stderr.String(), "ERROR:CONSOLE") {
			t.Fatal(stderr.String())
		}
		dom := string(body)
		labels, title := strings.Index(dom, ">Labels</th>"), strings.Index(dom, ">Title</th>")
		if labels < 0 || title < labels || !strings.Contains(dom, "Last updated") || strings.Contains(dom, "Not updated since") {
			t.Fatal(dom)
		}
		for _, text := range []string{"Report slice", "Which format?", "Per approver (cap", "item_added"} {
			if !strings.Contains(dom, text) {
				t.Errorf("rendered board missing %q", text)
			}
		}
		if regexp.MustCompile(`(?i)<(form|button|input|select|textarea)\b`).MatchString(dom) {
			t.Fatal("rendered board has mutation controls")
		}
		return dom
	}
	assertRow := func(dom, ref, labels, title string) {
		t.Helper()
		for _, row := range regexp.MustCompile(`(?s)<tr>.*?</tr>`).FindAllString(dom, -1) {
			if strings.Contains(row, " "+ref+"</td>") {
				if !strings.Contains(row, `<td class="wrap">`+labels+`</td><td class="wrap">`+title+`</td>`) {
					t.Fatalf("unexpected row for %s: %s", ref, row)
				}
				return
			}
		}
		t.Fatalf("missing rendered row for %s", ref)
	}
	initial := render()
	assertRow(initial, "ACME-1", "business, slow", "Report")
	assertRow(initial, "ACME-2", longLabel+", consumer", "Export")
	assertRow(initial, "ACME-3", "reloads-often", "Import")
	assertRow(initial, "ACME-4", "-", "Docs")
	if !reflect.DeepEqual(before, eng.EventsAfter(0, 0)) {
		t.Fatal("page reads changed log")
	}
	mu.Lock()
	change, pages, reads = true, 0, 0
	mu.Unlock()
	updated := render()
	assertRow(updated, "ACME-1", "business, slow", "Report")
	assertRow(updated, "ACME-2", "report", "Export")
	assertRow(updated, "ACME-3", "-", "Import")
	assertRow(updated, "ACME-4", "-", "Docs")
	mu.Lock()
	if pages != 1 || reads < 2 {
		t.Errorf("expected a refresh without navigation: %d page loads, %d board reads", pages, reads)
	}
	mu.Unlock()
	after := eng.EventsAfter(0, 0)
	if len(after) != len(before)+2 || !reflect.DeepEqual(before, after[:len(before)]) || after[len(before)].Type != flow.EvItemUpdated || after[len(before)+1].Type != flow.EvItemUpdated {
		t.Fatal("unexpected events during page refresh")
	}
}
