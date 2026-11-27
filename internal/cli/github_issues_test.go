package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/cli"
	"github.com/jystringfellow/metronome/internal/github"
)

const issuesJSON = `[
 {"number":1,"title":"Import","body":null,"created_at":"2026-11-25T09:00:00Z","assignees":[]},
 {"number":2,"title":"Export","body":"Metronome: MET-17\nExport records.\n## Acceptance criteria\n- CSV\n  with headers","created_at":"2026-11-26T09:00:00Z","assignees":[{"login":"rev"}],"labels":[{"name":"tier:0"}]},
 {"number":3,"title":"PR","pull_request":{"url":"https://example.com/pr/3"}}
]`

const projectJSON = `{"data":{"repositoryOwner":{"projectV2":{"field":{"__typename":"ProjectV2Field","dataType":"NUMBER"},"items":{"pageInfo":{"hasNextPage":false},"nodes":[{"content":{"__typename":"Issue","number":2,"repository":{"nameWithOwner":"acme/app"}},"fieldValueByName":{"__typename":"ProjectV2ItemFieldNumberValue","number":1}}]}}}}}`

func TestGitHubIssuesPrintsABacklogForIntake(t *testing.T) {
	e := newEnv(t)
	var calls [][]string
	gh := github.GH{Run: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		if args[1] == "graphql" {
			return []byte(projectJSON), nil
		}
		return []byte(issuesJSON), nil
	}}
	var out, stderr bytes.Buffer
	code := cli.RunWithDeps(context.Background(), []string{"github", "issues", "--repo", "acme/app", "--project", "acme/3", "--rank-field", "Rank", "--owner", "pat"}, &out, &stderr, cli.Deps{HTTP: e.http, Issues: gh})
	if code != 0 || stderr.Len() != 0 {
		t.Fatal(code, stderr.String())
	}
	var req api.IntakeRequest
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Items == nil || len(*req.Items) != 2 {
		t.Fatal(out.String(), err)
	}
	items := *req.Items
	if items[0].Ref != "MET-17" || items[1].Ref != "acme/app#1" || items[0].Owner != "rev" || items[1].Owner != "pat" || items[0].PredictedTier == nil || *items[0].PredictedTier != 0 || !reflect.DeepEqual(items[0].Slices[0].Criteria, []string{"CSV with headers"}) {
		t.Fatal(items)
	}
	if len(calls) != 2 || !reflect.DeepEqual(calls[0], []string{"api", "--paginate", "repos/acme/app/issues?direction=asc&labels=metronome&per_page=100&sort=created&state=open"}) || len(calls[1]) != 11 || !reflect.DeepEqual(calls[1][:4], []string{"api", "graphql", "--paginate", "-f"}) || !strings.HasPrefix(calls[1][4], "query=") || !reflect.DeepEqual(calls[1][5:], []string{"-f", "owner=acme", "-F", "number=3", "-f", "field=Rank"}) {
		t.Fatal(calls)
	}
	file := filepath.Join(e.dir, "issues.json")
	if err := os.WriteFile(file, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"intake", "--workstream", "default", "--", "cat", file}
	if result := e.ok("pat", args...); strings.Count(result, "item_added") != 2 {
		t.Fatal(result)
	}
	if result := e.ok("pat", args...); !strings.Contains(result, "no change") {
		t.Fatal(result)
	}
	board := e.board()
	if len(board.Items) != 2 || board.Items[0].Ref != "MET-17" || board.Items[1].Ref != "acme/app#1" {
		t.Fatal(board.Items)
	}
}

func TestGitHubIssuesWithoutProjectAndFailures(t *testing.T) {
	for _, test := range []struct {
		name, data, owner, message string
		failure                    bool
		want                       []string
	}{
		{"oldest", issuesJSON, "pat", "", false, []string{"acme/app#1", "MET-17"}},
		{"empty", `[]`, "", "", false, []string{}},
		{"no owner", issuesJSON, "", "has no assignee", false, nil},
		{"gh fails", "", "", "gh failed", true, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			gh := github.GH{Run: func(_ context.Context, args ...string) ([]byte, error) {
				calls++
				if args[1] != "--paginate" {
					t.Fatal(args)
				}
				if test.failure {
					return nil, errors.New("gh failed")
				}
				return []byte(test.data), nil
			}}
			var out, stderr bytes.Buffer
			code := cli.RunWithDeps(context.Background(), []string{"github", "issues", "--repo", "acme/app", "--owner", test.owner}, &out, &stderr, cli.Deps{Issues: gh})
			if calls != 1 {
				t.Fatal(calls)
			}
			if test.message != "" {
				if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), test.message) {
					t.Fatal(code, out.String(), stderr.String())
				}
				return
			}
			var req api.IntakeRequest
			if err := json.Unmarshal(out.Bytes(), &req); code != 0 || err != nil || req.Items == nil {
				t.Fatal(code, out.String(), stderr.String(), err)
			}
			refs := []string{}
			for _, item := range *req.Items {
				refs = append(refs, item.Ref)
			}
			if !reflect.DeepEqual(refs, test.want) {
				t.Fatal(refs)
			}
		})
	}
}

func TestGitHubIssuesUsage(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--repo", "acme"}, {"--repo", "acme/app/x"}, {"--repo", "acme/app", "--project", "acme"}, {"--repo", "acme/app", "--project", "acme/0"}, {"--repo", "acme/app", "--rank-field", "Rank"}, {"--repo", "acme/app", "--label", ""}, {"--repo", "acme/app", "--label", "a,b"}, {"--repo", "acme/app", "extra"},
	} {
		var out, stderr bytes.Buffer
		gh := github.GH{Run: func(_ context.Context, args ...string) ([]byte, error) {
			t.Fatalf("unexpected gh call: %v", args)
			return nil, nil
		}}
		code := cli.RunWithDeps(context.Background(), append([]string{"github", "issues"}, args...), &out, &stderr, cli.Deps{Issues: gh})
		if code != 2 || out.Len() != 0 {
			t.Fatal(args, code, out.String(), stderr.String())
		}
	}
	var out, stderr bytes.Buffer
	if code := cli.Run(context.Background(), []string{"help"}, &out, &stderr); code != 0 || !strings.Contains(out.String()+stderr.String(), "github issues --repo OWNER/NAME") {
		t.Fatal(code, out.String(), stderr.String())
	}
}

func TestGitHubIssuesRunsGHFromPath(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$2\" in\n  graphql) test \"${11}\" = field=Rank || exit 1; printf '%s\\n' '" + projectJSON + "';;\n  --paginate) printf '%s\\n' '" + issuesJSON + "';;\n  *) exit 1;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out, stderr bytes.Buffer
	if code := cli.Run(context.Background(), []string{"github", "issues", "--repo", "acme/app", "--project", "acme/3", "--owner", "pat"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "MET-17") {
		t.Fatal(code, out.String(), stderr.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\necho 'permission denied' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), []string{"github", "issues", "--repo", "acme/app", "--owner", "pat"}, &out, &stderr); code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "permission denied") {
		t.Fatal(code, out.String(), stderr.String())
	}
}
