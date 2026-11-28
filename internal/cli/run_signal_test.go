package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jystringfellow/metronome/internal/api"
)

type closeSignalTransport func(*http.Request) (*http.Response, error)

func (transport closeSignalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

func TestRunCloseIgnoresBufferedCommandSignal(t *testing.T) {
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				signals := make(chan os.Signal, 1)
				signals <- sig
				now := time.Now()
				lease := now.Add(15 * time.Minute)
				b := brief{Claim: briefClaim{RunID: "R1", SliceID: "I1.1", LeaseUntil: lease}}
				var stderr bytes.Buffer
				attempts := 0
				client := &api.Client{BaseURL: "http://example.com", Actor: "laptop", HTTP: &http.Client{
					Transport: closeSignalTransport(func(req *http.Request) (*http.Response, error) {
						synctest.Wait()
						if err := req.Context().Err(); err != nil {
							return nil, err
						}
						if req.Method != http.MethodPost || req.URL.Path != "/v1/runs/R1/release" {
							t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
						}
						attempts++
						response := httptest.NewRecorder()
						if attempts == 1 {
							http.Error(response, "unavailable", http.StatusServiceUnavailable)
						} else {
							response.WriteHeader(http.StatusOK)
						}
						return response.Result(), nil
					}),
				}}
				app := app{errOut: &stderr, client: client, now: func() time.Time { return now },
					wait: func(ctx context.Context, delay time.Duration) error {
						now = now.Add(delay)
						return ctx.Err()
					}}
				err := app.closeAfterExit(context.Background(), b, "laptop", "", "command interrupted", lease, signals)
				if err != nil || attempts != 2 || !strings.Contains(stderr.String(), "dropped R1") {
					t.Fatalf("close returned %v after %d attempts: %s", err, attempts, &stderr)
				}
			})
		})
	}
}
