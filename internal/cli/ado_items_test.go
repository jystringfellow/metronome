package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jystringfellow/metronome/internal/ado"
	"github.com/jystringfellow/metronome/internal/api"
	"github.com/jystringfellow/metronome/internal/cli"
)

const adoQuery = `{"queryType":"flat","queryResultType":"workItem","workItems":[{"id":1},{"id":2},{"id":3},{"id":4},{"id":5}]}`
const adoBatch = `{"count":5,"value":[{"id":1,"fields":{"System.Title":"Export","System.State":"New","System.Description":"<p>Export &quot;all&quot; &lt;records&gt;</p><p>Repo: acme/app</p>","Microsoft.VSTS.Common.AcceptanceCriteria":"<ul><li>CSV &amp; JSON</li><li>Keep &lt;tags&gt;</li></ul>","System.Tags":"tier:1; export","Microsoft.VSTS.Common.BacklogPriority":1},"relations":[{"rel":"System.LinkTypes.Dependency-Reverse","url":"https://dev.azure.com/example/_apis/wit/workItems/5"},{"rel":"System.LinkTypes.Dependency-Reverse","url":"https://dev.azure.com/example/_apis/wit/workItems/99"}]},{"id":2,"fields":{"System.Title":"Finished","System.State":"Done"}},{"id":3,"fields":{"System.Title":"Claimed","System.State":"New","System.AssignedTo":{"displayName":"rev"}}},{"id":4,"fields":{"System.Title":"Committed","System.State":"Committed"}},{"id":5,"fields":{"System.Title":"Import","System.State":"Approved"}}]}`
const adoGUID = "12345678-1234-1234-1234-123456789abc"

func adoArgs() []string {
	return []string{"ado", "items", "--org", "https://dev.azure.com/example/", "--project", "acme", "--query", adoGUID, "--owner", "pat"}
}

func TestADOItemsPrintsBacklogForIntake(t *testing.T) {
	e := newEnv(t)
	file := filepath.Join(e.dir, "items.wiql")
	if err := os.WriteFile(file, []byte("SELECT [System.Id] FROM WorkItems"), 0600); err != nil {
		t.Fatal(err)
	}
	args := adoArgs()
	args[6], args[7] = "--wiql", file
	var calls []string
	az := ado.AZ{Run: func(_ context.Context, args ...string) ([]byte, error) {
		if args[3] != "https://dev.azure.com/example" || args[9] != "project=acme" {
			t.Fatal(args)
		}
		resource := args[7]
		calls = append(calls, resource)
		if resource == "wiql" {
			return []byte(adoQuery), nil
		}
		return []byte(adoBatch), nil
	}}
	var out, stderr bytes.Buffer
	code := cli.RunWithDeps(context.Background(), args, &out, &stderr, cli.Deps{HTTP: e.http, Boards: az})
	if code != 0 || !slices.Equal(calls, []string{"wiql", "workitemsbatch"}) {
		t.Fatal(code, stderr.String(), calls)
	}
	for _, note := range []string{"AB#2 is Done", "AB#1 waits on AB#5 (in this result)", "AB#1 waits on AB#99, which is outside"} {
		if !strings.Contains(stderr.String(), note) {
			t.Fatal(stderr.String())
		}
	}
	var req api.IntakeRequest
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Items == nil || len(*req.Items) != 4 {
		t.Fatal(out.String(), err)
	}
	items := *req.Items
	if items[0].Intent != `Export "all" <records>` || !slices.Equal(items[0].Slices[0].Criteria, []string{"CSV & JSON", "Keep <tags>"}) || items[1].ClaimedBy != "rev" || items[2].ClaimedBy != "state Committed" {
		t.Fatal(items)
	}
	file = filepath.Join(e.dir, "ado.json")
	if err := os.WriteFile(file, out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	intake := []string{"intake", "--workstream", "default", "--", "cat", file}
	if result := e.ok("pat", intake...); strings.Count(result, "item_added") != 2 || !strings.Contains(result, "claimed") {
		t.Fatal(result)
	}
	if result := e.ok("pat", intake...); !strings.Contains(result, "no change") {
		t.Fatal(result)
	}
	board := e.board()
	if len(board.Items) != 2 || board.Items[0].Ref != "AB#1" || board.Items[1].Ref != "AB#5" || !slices.Equal(board.Items[0].Labels, []string{"export"}) {
		t.Fatal(board.Items)
	}
}

func TestADOItemsFailuresAndEmpty(t *testing.T) {
	for _, test := range []struct {
		name, data, owner string
		fail              bool
		want              int
	}{
		{"empty", `{"queryResultType":"workItem","workItems":[]}`, "", false, 0},
		{"links", `{"queryResultType":"workItemLink","workItems":[]}`, "pat", false, 1},
		{"no owner", adoQuery, "", false, 1},
		{"az error", "", "pat", true, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := adoArgs()
			args[9] = test.owner
			az := ado.AZ{Run: func(_ context.Context, args ...string) ([]byte, error) {
				if test.fail {
					return nil, errors.New("az denied")
				}
				if args[7] == "wiql" {
					if !slices.Contains(args, "id="+adoGUID) || !slices.Contains(args, "GET") {
						t.Fatal(args)
					}
					return []byte(test.data), nil
				}
				return []byte(adoBatch), nil
			}}
			var out, stderr bytes.Buffer
			if code := cli.RunWithDeps(context.Background(), args, &out, &stderr, cli.Deps{Boards: az}); code != test.want {
				t.Fatal(code, stderr.String())
			}
			if test.want == 1 && out.Len() != 0 || test.want == 0 && strings.ReplaceAll(strings.Join(strings.Fields(out.String()), ""), " ", "") != `{"items":[]}` {
				t.Fatal(out.String())
			}
		})
	}
}

func TestADOItemsUsage(t *testing.T) {
	for _, args := range [][]string{{"ado"}, {"ado", "unknown"}, {"ado", "items"}, {"ado", "items", "--org", "bad", "--project", "acme", "--query", adoGUID}, append(adoArgs(), "--wiql", "missing"), append(adoArgs(), "--query", "bad"), append(adoArgs(), "--project", " "), append(adoArgs(), "--ready-states", "New,"), {"ado", "items", "--org", "https://dev.azure.com/example", "--project", "acme", "--wiql", "missing"}} {
		var out, stderr bytes.Buffer
		az := ado.AZ{Run: func(_ context.Context, args ...string) ([]byte, error) { t.Fatal(args); return nil, nil }}
		if code := cli.RunWithDeps(context.Background(), args, &out, &stderr, cli.Deps{Boards: az}); code != 2 || out.Len() != 0 {
			t.Fatal(args, code, out.String(), stderr.String())
		}
	}
	var out, stderr bytes.Buffer
	if code := cli.Run(context.Background(), []string{"help"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), "ado items --org URL") {
		t.Fatal(code, out.String())
	}
}

func TestADOItemsRunsAZFromPath(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "az")
	text := "#!/bin/sh\ncase \"$8\" in\nwiql) printf '%s\\n' '" + adoQuery + "';;\nworkitemsbatch) printf '%s\\n' '" + adoBatch + "';;\n*) exit 1;;\nesac\n"
	if err := os.WriteFile(script, []byte(text), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out, stderr bytes.Buffer
	if code := cli.Run(context.Background(), adoArgs(), &out, &stderr); code != 0 || !strings.Contains(out.String(), "AB#1") {
		t.Fatal(code, out.String(), stderr.String())
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'permission denied' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	if code := cli.Run(context.Background(), adoArgs(), &out, &stderr); code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "permission denied") {
		t.Fatal(code, out.String(), stderr.String())
	}
}
