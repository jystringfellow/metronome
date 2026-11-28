package ado

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"
)

const queryFixture = `{"queryType":"flat","queryResultType":"workItem","workItems":[{"id":2},{"id":1}]}`
const batchFixture = `{"count":2,"value":[{"id":1,"fields":{"System.Title":"Export","System.State":"New","System.Description":"<p>&quot;Export&quot; &lt;records&gt;</p>","Microsoft.VSTS.Common.AcceptanceCriteria":"<ul><li>CSV</li></ul>","System.Tags":" tier:0; export ; export ","Microsoft.VSTS.Common.BacklogPriority":1.5},"relations":[{"rel":"System.LinkTypes.Dependency-Reverse","url":"https://dev.azure.com/example/_apis/wit/workItems/2"},{"rel":"System.LinkTypes.Dependency-Forward","url":"https://dev.azure.com/example/_apis/wit/workItems/5"}]},{"id":2,"fields":{"System.Title":"Import","System.State":"Active","System.AssignedTo":{"uniqueName":"rev@example.com","displayName":"rev"},"Microsoft.VSTS.Common.StackRank":2.5}}]}`

func TestParseQuery(t *testing.T) {
	ids, err := ParseQuery([]byte(queryFixture))
	if err != nil || !reflect.DeepEqual(ids, []int{2, 1}) {
		t.Fatal(ids, err)
	}
	ids, err = ParseQuery([]byte(`{"queryResultType":"workItem","workItems":[]}`))
	if err != nil || ids == nil || len(ids) != 0 {
		t.Fatal(ids, err)
	}
	for _, data := range []string{`{`, `{"queryResultType":"workItem"}`, `{"queryResultType":"workItemLink","workItems":[]}`, `{"queryResultType":"workItem","workItems":[{"id":1},{"id":1}]}`, `{"queryResultType":"workItem","workItems":[{"id":0}]}`} {
		if _, err := ParseQuery([]byte(data)); err == nil {
			t.Fatal(data)
		}
	}
}

func TestParseWorkItems(t *testing.T) {
	items, err := ParseWorkItems([]byte(batchFixture))
	if err != nil || len(items) != 2 {
		t.Fatal(items, err)
	}
	if items[0].Title != "Export" || *items[0].Rank != 1.5 || !reflect.DeepEqual(items[0].Tags, []string{"tier:0", "export", "export"}) || !reflect.DeepEqual(items[0].Predecessors, []int{2}) || items[1].AssignedTo != "rev@example.com" || *items[1].Rank != 2.5 {
		t.Fatal(items)
	}
	for _, data := range []string{`{"count":1,"value":[]}`, `{"count":1,"value":[{"id":1}]}`, `{"count":1,"value":[{"id":1,"fields":{"System.State":"New"},"relations":[{"rel":"System.LinkTypes.Dependency-Reverse","url":"https://example.com/bad"}]}]}`} {
		if _, err := ParseWorkItems([]byte(data)); err == nil {
			t.Fatal(data)
		}
	}
}

func TestParseWorkItemPRLinks(t *testing.T) {
	items, err := ParseWorkItems([]byte(`{"count":1,"value":[{"id":1,"fields":{"System.Title":"Export","System.State":"New"},"relations":[{"rel":"ArtifactLink","url":"vstfs:///GitHub/PullRequest/example%2F12","attributes":{"name":"GitHub Pull Request"}},{"rel":"ArtifactLink","url":"vstfs:///Git/PullRequestId/example%2F13","attributes":{"name":"Pull Request"}},{"rel":"ArtifactLink","url":"https://example.com/commit","attributes":{"name":"Commit"}}]}]}`))
	if err != nil || len(items) != 1 || items[0].LinkedPRs != 2 {
		t.Fatal(items, err)
	}
}

func TestAZArgumentsAndCleanup(t *testing.T) {
	const org = "https://dev.azure.com/example"
	const project = "acme"
	const guid = "12345678-1234-1234-1234-123456789abc"
	base := []string{"devops", "invoke", "--org", org, "--area", "wit", "--resource"}
	suffix := []string{"--api-version", "7.1", "--detect", "false", "--output", "json"}
	for _, mode := range []string{"wiql", "saved", "batch", "failed"} {
		t.Run(mode, func(t *testing.T) {
			var file string
			az := AZ{Run: func(_ context.Context, args ...string) ([]byte, error) {
				resource := "wiql"
				if mode == "batch" {
					resource = "workitemsbatch"
				}
				want := append(slices.Clone(base), resource, "--route-parameters", "project="+project)
				if mode == "saved" {
					want = append(want, "id="+guid, "--http-method", "GET")
				} else {
					file = args[slices.Index(args, "--in-file")+1]
					want = append(want, "--http-method", "POST", "--in-file", file)
					data, err := os.ReadFile(file)
					if err != nil {
						t.Fatal(err)
					}
					body := `{"query":"SELECT [System.Id] FROM WorkItems"}`
					if mode == "batch" {
						body = `{"$expand":"relations","errorPolicy":"fail","ids":[2,1]}`
					}
					if string(data) != body {
						t.Fatal(string(data))
					}
					info, err := os.Stat(file)
					if err != nil || info.Mode().Perm() != 0600 {
						t.Fatal(info, err)
					}
				}
				want = append(want, suffix...)
				if !slices.Equal(args, want) {
					t.Fatal(args, want)
				}
				if mode == "failed" {
					return nil, errors.New("denied")
				}
				if mode == "batch" {
					return []byte(batchFixture), nil
				}
				return []byte(queryFixture), nil
			}}
			var err error
			if mode == "batch" {
				var items []WorkItem
				items, err = az.WorkItems(context.Background(), org, project, []int{2, 1})
				if err == nil && (items[0].ID != 2 || items[1].ID != 1) {
					t.Fatal(items)
				}
			} else {
				target := Target{Org: org, Project: project, WIQL: "SELECT [System.Id] FROM WorkItems"}
				if mode == "saved" {
					target.QueryID = guid
				}
				_, err = az.Query(context.Background(), target)
			}
			if (err != nil) != (mode == "failed") {
				t.Fatal(err)
			}
			if file != "" {
				if _, err := os.Stat(file); !os.IsNotExist(err) {
					t.Fatal("temporary file remains", err)
				}
			}
		})
	}
}

func TestAZBatches(t *testing.T) {
	ids := make([]int, 201)
	for index := range ids {
		ids[index] = index + 1
	}
	for _, drop := range []bool{false, true} {
		var sizes []int
		az := AZ{Run: func(_ context.Context, args ...string) ([]byte, error) {
			data, err := os.ReadFile(args[slices.Index(args, "--in-file")+1])
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				IDs []int `json:"ids"`
			}
			if err := json.Unmarshal(data, &body); err != nil {
				t.Fatal(err)
			}
			sizes = append(sizes, len(body.IDs))
			var entries []json.RawMessage
			for _, id := range slices.Backward(body.IDs) {
				if drop && id == 1 {
					continue
				}
				entries = append(entries, json.RawMessage(fmt.Sprintf(`{"id":%d,"fields":{"System.State":"New"}}`, id)))
			}
			return json.Marshal(map[string]any{"count": len(entries), "value": entries})
		}}
		items, err := az.WorkItems(context.Background(), "https://dev.azure.com/example", "acme", ids)
		if drop {
			if err == nil {
				t.Fatal("missing ID accepted")
			}
			continue
		}
		if err != nil || !slices.Equal(sizes, []int{200, 1}) || len(items) != 201 {
			t.Fatal(sizes, len(items), err)
		}
		for index, item := range items {
			if item.ID != ids[index] {
				t.Fatal(item)
			}
		}
	}
}
