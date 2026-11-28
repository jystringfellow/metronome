package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T) (*rsa.PrivateKey, map[string]string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key, map[string]string{prefix + "ID": "123", prefix + "INSTALLATION_ID": "456", prefix + "PRIVATE_KEY": string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))}
}

func loadFixture(t *testing.T, vars map[string]string) *Source {
	t.Helper()
	s, err := Load(func(name string) string { return vars[name] }, nil)
	if err != nil || s == nil {
		t.Fatal("fixture configuration failed", err)
	}
	return s
}

func TestLoad(t *testing.T) {
	if s, err := Load(func(string) string { return "" }, nil); s != nil || err != nil {
		t.Fatal("empty configuration")
	}
	key, vars := fixture(t)
	data, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	delete(vars, prefix+"PRIVATE_KEY")
	vars[prefix+"PRIVATE_KEY_FILE"] = "example-key"
	s, err := Load(func(name string) string { return vars[name] }, func(path string) ([]byte, error) {
		return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: data}), nil
	})
	if err != nil || s == nil {
		t.Fatal("PKCS8 fixture failed", err)
	}
	vars[prefix+"PRIVATE_KEY"] = "invalid-fixture"
	if _, err := Load(func(name string) string { return vars[name] }, nil); err == nil || strings.Contains(err.Error(), "invalid-fixture") {
		t.Fatal("expected sanitized configuration failure")
	}
	delete(vars, prefix+"PRIVATE_KEY_FILE")
	if _, err := Load(func(name string) string { return vars[name] }, nil); err == nil || strings.Contains(err.Error(), "invalid-fixture") {
		t.Fatal("expected sanitized key failure")
	}
}

func TestLoadValidationAndEnvironment(t *testing.T) {
	_, vars := fixture(t)
	for _, test := range []struct{ name, value, rejected string }{
		{prefix + "ID", "", prefix + "ID"},
		{prefix + "INSTALLATION_ID", "", prefix + "INSTALLATION_ID"},
		{prefix + "PRIVATE_KEY", "", prefix + "PRIVATE_KEY"},
		{prefix + "INSTALLATION_ID", "0", prefix + "INSTALLATION_ID"},
		{prefix + "INSTALLATION_ID", "-2", prefix + "INSTALLATION_ID"},
		{prefix + "INSTALLATION_ID", "invalid-number", prefix + "INSTALLATION_ID"},
		{"METRONOME_GITHUB_API_URL", "http://example.com", "METRONOME_GITHUB_API_URL"},
	} {
		original := vars[test.name]
		vars[test.name] = test.value
		_, err := Load(func(name string) string { return vars[name] }, nil)
		vars[test.name] = original
		if err == nil || !strings.Contains(err.Error(), test.rejected) || (test.value != "" && strings.Contains(err.Error(), test.value)) {
			t.Fatalf("invalid %s was not safely rejected", test.name)
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	rsaKey := vars[prefix+"PRIVATE_KEY"]
	vars[prefix+"PRIVATE_KEY"] = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}))
	if _, err := Load(func(name string) string { return vars[name] }, nil); err == nil || strings.Contains(err.Error(), "-----BEGIN") || strings.Contains(err.Error(), base64.StdEncoding.EncodeToString(encoded)) {
		t.Fatal("non-RSA key was not safely rejected")
	}
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(rsaKey), 0600); err != nil {
		t.Fatal(err)
	}
	delete(vars, prefix+"PRIVATE_KEY")
	vars[prefix+"PRIVATE_KEY_FILE"] = path
	for _, base := range []string{"", "https://API.GITHUB.COM", "http://LOCALHOST:1234"} {
		vars["METRONOME_GITHUB_API_URL"] = base
		s := loadFixture(t, vars)
		s.token, s.refreshAt = "fixture-token", time.Now().Add(time.Hour)
		env, err := s.Environ(context.Background(), []string{"GH_ENTERPRISE_TOKEN=stale"})
		want := []string{"GH_TOKEN=fixture-token"}
		if strings.HasPrefix(base, "http:") {
			want = append(want, "GH_ENTERPRISE_TOKEN=fixture-token")
		}
		if err != nil || !reflect.DeepEqual(env, want) {
			t.Fatal("unexpected API host environment", err)
		}
	}
}

func TestTokenRefresh(t *testing.T) {
	key, vars := fixture(t)
	now := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	var calls, failures int
	status := http.StatusInternalServerError
	var waits []time.Duration
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/app/installations/456/access_tokens" || r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Error("incorrect token request")
		}
		jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(jwt, ".")
		if len(parts) != 3 {
			t.Error("incorrect signing format")
			w.WriteHeader(400)
			return
		}
		sig, err := base64.RawURLEncoding.DecodeString(parts[2])
		digest := sha256.Sum256([]byte(strings.Join(parts[:2], ".")))
		if err != nil || rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig) != nil {
			t.Error("invalid signing signature")
		}
		claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var decoded struct {
			Iss      string
			Iat, Exp int64
		}
		if json.Unmarshal(claims, &decoded) != nil || decoded.Iss != "123" || decoded.Iat != now.Add(-time.Minute).Unix() || decoded.Exp != now.Add(9*time.Minute).Unix() {
			t.Error("incorrect signing claims")
		}
		if failures > 0 {
			failures--
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]string{"message": jwt + " fixture-one"})
			return
		}
		w.WriteHeader(201)
		token := "fixture-one"
		if calls > 1 {
			token = "fixture-two"
		}
		json.NewEncoder(w).Encode(map[string]any{"token": token, "expires_at": now.Add(time.Hour)})
	}))
	defer server.Close()
	vars["METRONOME_GITHUB_API_URL"] = server.URL
	s := loadFixture(t, vars)
	s.Now = func() time.Time { return now }
	s.Wait = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return ctx.Err() }
	var log bytes.Buffer
	s.Log = &log
	ctx := context.Background()
	check := func(want string) {
		t.Helper()
		got, err := s.Token(ctx)
		if err != nil || got != want {
			t.Fatal("unexpected token result", err)
		}
	}
	check("fixture-one")
	now = now.Add(49 * time.Minute)
	check("fixture-one")
	if calls != 1 {
		t.Fatal("cache missed")
	}
	now = now.Add(2 * time.Minute)
	failures = 2
	check("fixture-two")
	if calls != 4 || !reflect.DeepEqual(waits, []time.Duration{time.Second, 2 * time.Second}) {
		t.Fatal("wrong retry schedule", calls, waits)
	}
	env, err := s.Environ(ctx, []string{"GH_TOKEN=old", "GITHUB_TOKEN=old", "GH_ENTERPRISE_TOKEN=old", prefix + "PRIVATE_KEY=private", "OTHER=example"})
	if err != nil || !reflect.DeepEqual(env, []string{"OTHER=example", "GH_TOKEN=fixture-two", "GH_ENTERPRISE_TOKEN=fixture-two"}) {
		t.Fatal("incorrect environment")
	}
	if !s.Holds([]byte("fixture-one")) || !s.Holds([]byte("fixture-two")) || s.Holds([]byte("ghs_"+strings.Repeat("x", 20)+" eyJexample.payload.signature")) || s.Redact("fixture-one fixture-two") != "[redacted] [redacted]" {
		t.Fatal("previous token not protected")
	}
	now = now.Add(51 * time.Minute)
	status = 401
	failures = 1
	check("fixture-two")
	if calls != 5 || !strings.Contains(log.String(), "HTTP 401") || strings.Contains(log.String(), "fixture-") || strings.Contains(log.String(), "eyJ") {
		t.Fatal("incorrect fallback")
	}
	now = now.Add(10 * time.Minute)
	status = 500
	failures = 4
	waits = nil
	if _, err := s.Token(ctx); err == nil || strings.Contains(err.Error(), "fixture-") || strings.Contains(err.Error(), "eyJ") {
		t.Fatal("expired token was accepted or error leaked")
	}
	if calls != 9 || !reflect.DeepEqual(waits, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}) {
		t.Fatal("incorrect retry exhaustion", calls, waits)
	}
	if strings.Contains(fmt.Sprintf("%v %+v %#v", s, s, s), "fixture-") || strings.Contains(fmt.Sprintf("%#v", s), "PRIVATE KEY") {
		t.Fatal("formatting leaks credentials")
	}
}

func TestRefreshPrunesExpiredTokens(t *testing.T) {
	_, vars := fixture(t)
	start := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	now := start
	expired := "ghs_" + strings.Repeat("x", 20)
	tokens := []string{expired, "fixture-previous", "fixture-current"}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"token": tokens[calls], "expires_at": now.Add(time.Hour)})
		calls++
	}))
	defer server.Close()
	vars["METRONOME_GITHUB_API_URL"] = server.URL
	source := loadFixture(t, vars)
	source.Now = func() time.Time { return now }
	for index, want := range tokens {
		now = start.Add(time.Duration(index) * 50 * time.Minute)
		if got, err := source.Token(t.Context()); err != nil || got != want {
			t.Fatal("expected a fresh token", err)
		}
	}
	if source.Holds([]byte(expired)) {
		t.Fatal("expired token still matches after refresh")
	}
	for _, token := range tokens[1:] {
		if !source.Holds([]byte(token)) {
			t.Fatal("unexpired token no longer matches after refresh")
		}
	}
	if source.Redact(strings.Join(tokens, " ")) != "[redacted] [redacted] [redacted]" {
		t.Fatal("expected format redaction for expired token and exact redaction for unexpired tokens")
	}
}

func TestRedactFormats(t *testing.T) {
	for _, value := range []string{
		"ghs_" + strings.Repeat("x", 20),
		"github_pat_" + strings.Repeat("x", 20),
		"eyJ" + strings.Repeat("x", 10) + ".payload.signature",
		"-----BEGIN " + "RSA PRIVATE KEY-----\nfixture\n-----END RSA PRIVATE KEY-----",
	} {
		if Redact("before "+value+" after") != "before [redacted] after" {
			t.Fatal("credential format not redacted")
		}
	}
	if got := WithoutAppSettings([]string{"GH_TOKEN=fixture", prefix + "PRIVATE_KEY=private", prefix + "ID=123"}); !reflect.DeepEqual(got, []string{"GH_TOKEN=fixture"}) {
		t.Fatal("App settings were not stripped")
	}
}

func TestTokenSingleFlight(t *testing.T) {
	_, vars := fixture(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"token": "fixture-token", "expires_at": time.Now().Add(time.Hour)})
	}))
	defer server.Close()
	vars["METRONOME_GITHUB_API_URL"] = server.URL
	s := loadFixture(t, vars)
	var group sync.WaitGroup
	for range 20 {
		group.Go(func() {
			if value, err := s.Token(context.Background()); err != nil || value != "fixture-token" {
				t.Error("token request failed")
			}
		})
	}
	group.Wait()
	if calls != 1 {
		t.Fatal("concurrent requests were not coalesced")
	}
}

func TestRefreshCancellationAndShortLifetime(t *testing.T) {
	_, vars := fixture(t)
	now := time.Now()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 2 {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"token": "fixture-token", "expires_at": now.Add(4 * time.Minute)})
	}))
	defer server.Close()
	vars["METRONOME_GITHUB_API_URL"] = server.URL
	s := loadFixture(t, vars)
	s.Now = func() time.Time { return now }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Wait = func(ctx context.Context, d time.Duration) error { cancel(); return ctx.Err() }
	if _, err := s.Token(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := s.Token(ctx); err != nil || calls != 1 {
		t.Fatal("early refresh")
	}
	now = now.Add(time.Minute)
	if _, err := s.Token(ctx); err != nil || calls != 2 {
		t.Fatal("missing half-life refresh")
	}
	now = now.Add(2 * time.Minute)
	if _, err := s.Token(ctx); err != context.Canceled {
		t.Fatal("cancellation was not returned", err)
	}
}
