package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/jystringfellow/metronome/internal/githubapp"
)

type appAuth struct {
	once   sync.Once
	getenv func(string) string
	source *githubapp.Source
	err    error
	log    io.Writer
}

func (a *app) loadGitHubApp() error {
	if a.auth == nil {
		return nil
	}
	a.auth.once.Do(func() {
		a.auth.source, a.auth.err = githubapp.Load(a.auth.getenv, os.ReadFile)
		if a.auth.err != nil {
			a.auth.err = usagef("%s", a.auth.err)
		}
		if a.auth.source != nil {
			a.auth.source.Log = a.auth.log
		}
	})
	return a.auth.err
}

func (a *app) githubEnv(ctx context.Context, base []string) ([]string, error) {
	if err := a.loadGitHubApp(); err != nil {
		return nil, err
	}
	if a.auth == nil || a.auth.source == nil {
		return nil, nil
	}
	return a.auth.source.Environ(ctx, base)
}

func (a *app) childEnv(ctx context.Context, extra ...string) ([]string, error) {
	base := os.Environ()
	env, err := a.githubEnv(ctx, base)
	if err != nil {
		return nil, fmt.Errorf("github app token: %w", err)
	}
	if env == nil {
		env = base
	}
	return append(env, extra...), nil
}

func (a *app) redact(value string) string {
	if a.auth != nil {
		return a.auth.source.Redact(value)
	}
	return githubapp.Redact(value)
}

func (a *app) holds(data []byte) bool {
	return a.auth != nil && a.auth.source != nil && a.auth.source.Holds(data)
}

type redactingOutput struct {
	writer     io.Writer
	redact     func(string) string
	pending    bytes.Buffer
	privateKey bool
}

func (w *redactingOutput) Write(data []byte) (int, error) {
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		w.pending.Write(line)
		if bytes.HasSuffix(line, []byte("\n")) {
			if err := w.flush(); err != nil {
				return 0, err
			}
		}
	}
	return len(data), nil
}

func (w *redactingOutput) flush() error {
	if w.pending.Len() == 0 {
		return nil
	}
	value := w.pending.String()
	if strings.Contains(value, "-----BEGIN ") && strings.Contains(value, "PRIVATE KEY-----") && !strings.Contains(value, "-----END ") {
		w.privateKey = true
		value = "[redacted]\n"
	} else if w.privateKey {
		if strings.Contains(value, "-----END ") && strings.Contains(value, "PRIVATE KEY-----") {
			w.privateKey = false
		}
		value = ""
	}
	_, err := io.WriteString(w.writer, w.redact(value))
	w.pending.Reset()
	return err
}
