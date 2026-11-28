package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/flow"
	"github.com/jystringfellow/metronome/internal/store"
)

func checkProbe(t *testing.T, server *httptest.Server, path string, code int, want string) []byte {
	t.Helper()
	resp, body := request(t, server, http.MethodGet, path)
	if resp.StatusCode != code || resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Cache-Control") != "no-store" || !strings.Contains(string(body), want) {
		t.Fatalf("%s: status=%d headers=%v body=%s", path, resp.StatusCode, resp.Header, body)
	}
	return body
}

func TestProbesBeforeReady(t *testing.T) {
	server := httptest.NewServer(api.NewService())
	defer server.Close()
	checkProbe(t, server, "/healthz", 200, `"status":"ok"`)
	checkProbe(t, server, "/readyz", 503, `"status":"starting"`)
	resp, body := request(t, server, http.MethodGet, "/v1/board")
	if resp.StatusCode != 503 || resp.Header.Get("Retry-After") != "1" || !strings.Contains(string(body), `"code":"unavailable"`) {
		t.Fatalf("board: %d %v %s", resp.StatusCode, resp.Header, body)
	}
	resp, body = request(t, server, http.MethodHead, "/healthz")
	if resp.StatusCode != 200 || len(body) != 0 {
		t.Fatalf("HEAD: %d %s", resp.StatusCode, body)
	}
}

func TestProbesAfterReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metronome.db")
	db, err := store.OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	eng, err := flow.NewEngine(t.Context(), flow.DefaultConfig(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if eng.LastSeq() != 0 {
		t.Fatal("nonzero empty sequence")
	}
	result, err := eng.SyncBacklog(t.Context(), "pat", "sync", "default", []flow.BacklogItem{{Ref: "ACME-1", Title: "first", Owner: "pat"}, {Ref: "ACME-2", Title: "second", Owner: "pat"}})
	if err != nil || len(result.Events) != 2 || eng.LastSeq() != 2 {
		t.Fatalf("sync: %+v err=%v seq=%d", result, err, eng.LastSeq())
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	eng, err = flow.NewEngine(t.Context(), flow.DefaultConfig(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := api.NewService()
	svc.Ready(eng, db)
	server := httptest.NewServer(svc)
	defer server.Close()
	checkProbe(t, server, "/readyz", 200, fmt.Sprintf(`"last_seq":%d`, len(result.Events)))
	checkProbe(t, server, "/healthz", 200, `"status":"ok"`)
	resp, body := request(t, server, http.MethodGet, "/v1/board")
	var board api.Board
	if err := json.Unmarshal(body, &board); err != nil || resp.StatusCode != 200 || len(board.Items) != 2 {
		t.Fatalf("board: %d %s %v", resp.StatusCode, body, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	body = checkProbe(t, server, "/readyz", 503, `"status":"unavailable"`)
	var failure map[string]string
	if err := json.Unmarshal(body, &failure); err != nil || failure["error"] == "" {
		t.Fatalf("failure: %s %v", body, err)
	}
	checkProbe(t, server, "/healthz", 200, `"status":"ok"`)
}
