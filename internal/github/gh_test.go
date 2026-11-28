package github

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jystringfellow/metronome/internal/githubapp"
)

func TestGHInstallationEnvironment(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_TOKEN", "operator-fixture")
	t.Setenv("METRONOME_GITHUB_APP_PRIVATE_KEY", "private-fixture")
	t.Setenv("GH_FIXTURE_OUTPUT", filepath.Join(dir, "output"))
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nprintf '%s:%s' \"$GH_TOKEN\" \"${METRONOME_GITHUB_APP_PRIVATE_KEY-unset}\" > \"$GH_FIXTURE_OUTPUT\"\nif [ \"$GH_FIXTURE_FAIL\" = yes ]; then printf '%s' \"$GH_TOKEN\" >&2; exit 1; fi\nprintf '[]'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	g := GH{Environ: func(_ context.Context, base []string) ([]string, error) {
		return githubapp.Environ(base, "app-fixture", false), nil
	}}
	if _, err := g.PRForBranch(context.Background(), "acme/app", "example"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(os.Getenv("GH_FIXTURE_OUTPUT")); err != nil || string(data) != "app-fixture:unset" {
		t.Fatal("wrong App environment", err)
	}
	t.Setenv("GH_FIXTURE_FAIL", "yes")
	if _, err := g.PRForBranch(context.Background(), "acme/app", "example"); err == nil || strings.Contains(err.Error(), "app-fixture") || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatal("error not redacted")
	}
	t.Setenv("GH_FIXTURE_FAIL", "")
	if _, err := (GH{}).PRForBranch(context.Background(), "acme/app", "example"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(os.Getenv("GH_FIXTURE_OUTPUT")); err != nil || string(data) != "operator-fixture:private-fixture" {
		t.Fatal("operator environment changed", err)
	}
}

func TestGHAcceptsUnrelatedTokenShapes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	shape := "ghs_" + strings.Repeat("x", 20)
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nprintf '%s' '[{\"number\":1,\"title\":\"example\",\"body\":\""+shape+"\"}]'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	source := &githubapp.Source{}
	gh := GH{Holds: source.Holds, Environ: func(_ context.Context, base []string) ([]string, error) {
		return githubapp.Environ(base, "app-fixture", false), nil
	}}
	issues, err := gh.OpenIssues(t.Context(), "acme/app", "example")
	if err != nil || len(issues) != 1 || issues[0].Body != shape {
		t.Fatal("unrelated example credential rejected", err)
	}
}
