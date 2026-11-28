package cli_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/cli"
	"github.com/jystringfellow/metronome/internal/flow"
)

func appFixture(t *testing.T) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("METRONOME_GITHUB_APP_ID", "1")
	t.Setenv("METRONOME_GITHUB_APP_INSTALLATION_ID", "2")
	t.Setenv("METRONOME_GITHUB_APP_PRIVATE_KEY", string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})))
	t.Setenv("METRONOME_GITHUB_APP_PRIVATE_KEY_FILE", "")
	t.Setenv("GH_TOKEN", "operator-fixture")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/installations/2/access_tokens" || r.Method != "POST" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("unexpected token request")
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"token": "app-fixture", "expires_at": time.Now().Add(time.Hour)})
	}))
	t.Cleanup(server.Close)
	t.Setenv("METRONOME_GITHUB_API_URL", server.URL)
}

func TestGitHubAppIntakeAndPublish(t *testing.T) {
	e := newEnv(t)
	e.ok("pat", "workstream", "create", "example")
	appFixture(t)
	base := []string{"intake", "--workstream", "example", "--", "sh", "-c"}
	shape := "ghs_" + strings.Repeat("x", 20) + " eyJexample.payload.signature"
	output := e.ok("pat", append(base, `test "$GH_TOKEN" = app-fixture && test "${METRONOME_GITHUB_APP_PRIVATE_KEY-unset}" = unset || exit 9; echo "$GH_TOKEN" >&2; printf '{"items":[{"ref":"ACME-1","title":"%s","owner":"pat"}]}' "$1"`, "sh", shape)...)
	if strings.Contains(output, "app-fixture") || !strings.Contains(output, "[redacted]") {
		t.Fatal("intake output leaked or missing redaction")
	}
	if items := e.board().Items; len(items) != 1 || items[0].Title != shape {
		t.Fatal("unrelated token-shaped intake content was not preserved")
	}
	before := e.ok("pat", "events")
	output, code := e.run("pat", append(base, `printf '{"items":[{"ref":"ACME-2","title":"%s","owner":"pat"}]}' "$GH_TOKEN"`)...)
	if code != 1 || strings.Contains(output, "app-fixture") || !strings.Contains(output, "nothing synced") || e.ok("pat", "events") != before {
		t.Fatal("intake accepted secret output")
	}
	e.ok("pat", "plan", "start", "--workstream", "example")
	publish := []string{"publish", "--workstream", "example", "--name", "board", "--", "sh", "-c"}
	output, code = e.run("pat", append(publish, `test "$GH_TOKEN" = app-fixture && test "${METRONOME_GITHUB_APP_PRIVATE_KEY-unset}" = unset || exit 9; printf '%s' "$GH_TOKEN" >&2; printf '"%s"' "$GH_TOKEN" > "$METRONOME_PUBLISHER_STATE"`)...)
	if code != 1 || strings.Contains(output, "app-fixture") || !strings.Contains(output, "cursor stays") || !strings.Contains(output, "[redacted]") {
		t.Fatal("publisher accepted secret state or leaked output")
	}
	var batch api.PublishBatch
	client := api.Client{BaseURL: "http://example.com", Actor: "pat", HTTP: e.http}
	if err := client.Get(context.Background(), "/v1/workstreams/example/publishers/board/batch?limit=100", &batch); err != nil || batch.Cursor != 0 || len(batch.Transitions) == 0 || bytes.Contains(batch.State, []byte("app-fixture")) {
		t.Fatal("publisher state or cursor changed", err)
	}
	e.ok("pat", append(publish, `test "$GH_TOKEN" = app-fixture && test "${METRONOME_GITHUB_APP_PRIVATE_KEY-unset}" = unset && cat >/dev/null`)...)
	for _, name := range []string{"ID", "INSTALLATION_ID", "PRIVATE_KEY", "PRIVATE_KEY_FILE"} {
		t.Setenv("METRONOME_GITHUB_APP_"+name, "")
	}
	e.ok("pat", append(base, `test "$GH_TOKEN" = operator-fixture && printf '%s' '{"items":[]}'`)...)
	e.ok("pat", "publish", "--workstream", "example", "--name", "other", "--", "sh", "-c", `test "$GH_TOKEN" = operator-fixture && cat >/dev/null`)
}

func TestGitHubAppPoll(t *testing.T) {
	e := newEnv(t)
	backlog := e.file("backlog.json", api.BacklogRequest{Items: []flow.BacklogItem{{Ref: "ACME-1", Title: "example", Owner: "pat", PredictedTier: intp(1)}}})
	e.ok("pat", "backlog", "sync", backlog)
	e.ok("pat", "plan", "start")
	slices := e.file("slices.json", []flow.SliceSpec{{Title: "example", Repo: "acme/app"}})
	e.ok("pat", "plan", "propose", "I1", "--slices", slices)
	e.ok("pat", "plan", "approve", "I1", "--tier", "1")
	e.ok("pat", "next", "--runner", "example")
	e.ok("pat", "submit", "R1", "--branch", "example")
	appFixture(t)
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	marker := filepath.Join(dir, "called")
	t.Setenv("GH_FIXTURE_MARKER", marker)
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\ntest \"$GH_TOKEN\" = app-fixture && test \"$GH_ENTERPRISE_TOKEN\" = app-fixture && test \"${METRONOME_GITHUB_APP_PRIVATE_KEY-unset}\" = unset || exit 9\ntouch \"$GH_FIXTURE_MARKER\"\nprintf '[]'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	apiURL := os.Getenv("METRONOME_GITHUB_API_URL")
	t.Setenv("METRONOME_GITHUB_API_URL", "https://api.github.com")
	deps := e.deps()
	deps.Getenv = func(name string) string {
		if name == "METRONOME_GITHUB_API_URL" {
			return apiURL
		}
		return os.Getenv(name)
	}
	var out, notes bytes.Buffer
	if code := cli.RunWithDeps(t.Context(), []string{"--server", "http://example.com", "--as", "pat", "github", "poll"}, &out, &notes, deps); code != 0 {
		t.Fatal("poll failed", code, notes.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("poll did not call gh", err)
	}
}

func TestGitHubAppEndpointFailure(t *testing.T) {
	e := newEnv(t)
	e.ok("pat", "workstream", "create", "example")
	appFixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
		json.NewEncoder(w).Encode(map[string]string{"message": r.Header.Get("Authorization") + " app-fixture"})
	}))
	defer server.Close()
	t.Setenv("METRONOME_GITHUB_API_URL", server.URL)
	before := e.ok("pat", "events")
	output, code := e.run("pat", "intake", "--workstream", "example", "--", "sh", "-c", `printf '%s' '{"items":[]}'`)
	if code != 1 || calls.Load() != 4 || !strings.Contains(output, "HTTP 500") || !strings.Contains(output, "nothing synced") || strings.Contains(output, "app-fixture") || strings.Contains(output, "eyJ") || e.ok("pat", "events") != before {
		t.Fatal("failed token round was not safely rejected")
	}
}

func TestGitHubAppInvalidSettings(t *testing.T) {
	e := newEnv(t)
	t.Setenv("METRONOME_GITHUB_APP_ID", "1")
	t.Setenv("METRONOME_GITHUB_APP_INSTALLATION_ID", "")
	output, code := e.run("pat", "serve", "--config", filepath.Join(e.dir, "instance.json"), "--db", filepath.Join(e.dir, "db"))
	if code != 2 || !strings.Contains(output, "METRONOME_GITHUB_APP_INSTALLATION_ID") {
		t.Fatal("partial configuration did not fail at startup")
	}
	t.Setenv("METRONOME_GITHUB_APP_INSTALLATION_ID", "2")
	t.Setenv("METRONOME_GITHUB_APP_PRIVATE_KEY", "invalid-private-fixture")
	output, code = e.run("pat", "serve", "--db", filepath.Join(e.dir, "db"))
	if code != 2 || strings.Contains(output, "invalid-private-fixture") {
		t.Fatal("invalid key configuration leaked")
	}
	e.ok("pat", "board")
}
