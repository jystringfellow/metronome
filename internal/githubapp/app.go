package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const prefix = "METRONOME_GITHUB_APP_"

type Source struct {
	appID, installationID, apiURL string
	key                           *rsa.PrivateKey
	enterprise                    bool
	lock                          chan struct{}
	mu                            sync.RWMutex
	issued                        map[string]time.Time
	token                         string
	expiresAt, refreshAt          time.Time
	Now                           func() time.Time
	Wait                          func(context.Context, time.Duration) error
	HTTP                          *http.Client
	Log                           io.Writer
}

func Load(getenv func(string) string, readFile func(string) ([]byte, error)) (*Source, error) {
	id, installation := getenv(prefix+"ID"), getenv(prefix+"INSTALLATION_ID")
	key, path := getenv(prefix+"PRIVATE_KEY"), getenv(prefix+"PRIVATE_KEY_FILE")
	if id == "" && installation == "" && key == "" && path == "" {
		return nil, nil
	}
	if id == "" || strings.ContainsFunc(id, unicode.IsSpace) {
		return nil, fmt.Errorf("%sID must be nonempty and contain no whitespace", prefix)
	}
	number, err := strconv.ParseUint(installation, 10, 64)
	if err != nil || number == 0 || strings.ContainsFunc(installation, func(r rune) bool { return r < '0' || r > '9' }) {
		return nil, fmt.Errorf("%sINSTALLATION_ID must be a positive decimal integer", prefix)
	}
	if (key == "") == (path == "") {
		return nil, fmt.Errorf("give exactly one of %sPRIVATE_KEY or %sPRIVATE_KEY_FILE", prefix, prefix)
	}
	data := []byte(key)
	if path != "" {
		if readFile == nil {
			readFile = os.ReadFile
		}
		data, err = readFile(path)
		if err != nil {
			return nil, fmt.Errorf("cannot read %sPRIVATE_KEY_FILE", prefix)
		}
	}
	block, _ := pem.Decode(data)
	var private *rsa.PrivateKey
	if block != nil {
		private, _ = x509.ParsePKCS1PrivateKey(block.Bytes)
		if private == nil {
			parsed, _ := x509.ParsePKCS8PrivateKey(block.Bytes)
			private, _ = parsed.(*rsa.PrivateKey)
		}
	}
	if private == nil || private.Validate() != nil {
		return nil, fmt.Errorf("%sPRIVATE_KEY is not a PEM RSA private key", prefix)
	}
	base := getenv("METRONOME_GITHUB_API_URL")
	if base == "" {
		base = "https://api.github.com"
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback(parsed.Hostname()))) {
		return nil, fmt.Errorf("METRONOME_GITHUB_API_URL must be an absolute HTTPS URL (HTTP only on loopback)")
	}
	return &Source{appID: id, installationID: strconv.FormatUint(number, 10), apiURL: strings.TrimRight(base, "/"), key: private, enterprise: !strings.EqualFold(parsed.Hostname(), "api.github.com"), lock: make(chan struct{}, 1), issued: make(map[string]time.Time), Now: time.Now, Wait: wait, HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func loopback(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func (s *Source) String() string {
	return fmt.Sprintf("GitHub App %s installation %s", Redact(s.appID), s.installationID)
}
func (s *Source) GoString() string { return s.String() }

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Source) jwt() (string, error) {
	now := s.Now()
	claims, err := json.Marshal(map[string]any{"iss": s.appID, "iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix()})
	if err != nil {
		return "", fmt.Errorf("cannot encode App JWT")
	}
	encode := base64.RawURLEncoding.EncodeToString
	unsigned := encode([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + encode(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("cannot sign App JWT")
	}
	return unsigned + "." + encode(signature), nil
}

func (s *Source) mint(ctx context.Context) (string, time.Time, bool, error) {
	jwt, err := s.jwt()
	if err != nil {
		return "", time.Time{}, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiURL+"/app/installations/"+s.installationID+"/access_tokens", nil)
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("cannot create installation token request")
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "metronome")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return "", time.Time{}, true, fmt.Errorf("installation token request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", time.Time{}, resp.StatusCode == 429 || resp.StatusCode >= 500, fmt.Errorf("installation token endpoint returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Token   string    `json:"token"`
		Expires time.Time `json:"expires_at"`
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &result) != nil || result.Token == "" || !result.Expires.After(s.Now()) || strings.ContainsAny(result.Token, "\x00\r\n") {
		return "", time.Time{}, false, fmt.Errorf("invalid installation token response")
	}
	return result.Token, result.Expires, false, nil
}

func (s *Source) Token(ctx context.Context) (string, error) {
	select {
	case s.lock <- struct{}{}:
		defer func() { <-s.lock }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.Now().Before(s.refreshAt) {
		return s.token, nil
	}
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			if err := s.Wait(ctx, time.Second<<uint(attempt-1)); err != nil {
				return "", err
			}
		}
		token, expiry, retry, err := s.mint(ctx)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err == nil {
			now := s.Now()
			s.token, s.expiresAt = token, expiry
			s.refreshAt = expiry.Add(-min(10*time.Minute, max(0, expiry.Sub(now)/2)))
			s.mu.Lock()
			for previous, expiresAt := range s.issued {
				if !expiresAt.After(now) {
					delete(s.issued, previous)
				}
			}
			s.issued[token] = expiry
			s.mu.Unlock()
			return token, nil
		}
		last = err
		if !retry {
			break
		}
	}
	if s.token != "" && s.expiresAt.After(s.Now().Add(time.Minute)) {
		if s.Log != nil {
			fmt.Fprintf(s.Log, "github app: token refresh failed; using the current token until %s: %s\n", s.expiresAt.Format(time.RFC3339), s.Redact(last.Error()))
		}
		return s.token, nil
	}
	return "", last
}

func WithoutAppSettings(base []string) []string {
	var env []string
	for _, entry := range base {
		if !strings.HasPrefix(entry, prefix) {
			env = append(env, entry)
		}
	}
	return env
}

func Environ(base []string, token string, enterprise bool) []string {
	var env []string
	for _, entry := range WithoutAppSettings(base) {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN":
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "GH_TOKEN="+token)
	if enterprise {
		env = append(env, "GH_ENTERPRISE_TOKEN="+token)
	}
	return env
}

func (s *Source) Environ(ctx context.Context, base []string) ([]string, error) {
	token, err := s.Token(ctx)
	if err != nil {
		return nil, err
	}
	return Environ(base, token, s.enterprise), nil
}

var secretPattern = regexp.MustCompile(`gh[oprsu]_[A-Za-z0-9_]{16,}|github_pat_[A-Za-z0-9_]{16,}|eyJ[\w-]+\.[\w-]+\.[\w-]+|(?s:-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----)`)

func Redact(value string) string { return secretPattern.ReplaceAllString(value, "[redacted]") }

func (s *Source) Redact(value string) string {
	if s != nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
		for token := range s.issued {
			value = strings.ReplaceAll(value, token, "[redacted]")
		}
	}
	return Redact(value)
}

func (s *Source) Holds(data []byte) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for token := range s.issued {
		if bytes.Contains(data, []byte(token)) {
			return true
		}
	}
	return false
}
