package github

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

const issuePage = `[
 {"number":4,"title":"Export","body":"Export records.\n- [ ] CSV","created_at":"2026-11-26T10:00:00Z","assignees":[{"login":"pat"},{"login":"rev"}],"labels":[{"name":"metronome"},{"name":"tier:2"}]},
 {"number":2,"title":"Import","body":null,"assignees":[],"created_at":"2026-11-25T10:00:00Z"},
 {"number":3,"title":"PR","pull_request":{"url":"https://example.com/pr/3"}}
]`

func TestGHAsksForOpenLabelledIssues(t *testing.T) {
	for label, escaped := range map[string]string{"metronome": "metronome", "needs triage": "needs+triage", "team:x": "team%3Ax"} {
		var got []string
		gh := GH{Run: func(_ context.Context, args ...string) ([]byte, error) { got = args; return []byte(issuePage), nil }}
		issues, err := gh.OpenIssues(context.Background(), "acme/app", label)
		want := []string{"api", "--paginate", "repos/acme/app/issues?direction=asc&labels=" + escaped + "&per_page=100&sort=created&state=open"}
		if err != nil || !reflect.DeepEqual(got, want) || len(issues) != 2 {
			t.Fatalf("args=%v issues=%v err=%v", got, issues, err)
		}
	}
}

func TestParseIssuesSkipsPullRequestsAndToleratesMissingFields(t *testing.T) {
	issues, err := ParseIssues([]byte(issuePage))
	want := []Issue{
		{Number: 4, Title: "Export", Body: "Export records.\n- [ ] CSV", CreatedAt: time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC), Assignees: []string{"pat", "rev"}, Labels: []string{"metronome", "tier:2"}},
		{Number: 2, Title: "Import", CreatedAt: time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)},
	}
	if err != nil || !reflect.DeepEqual(issues, want) {
		t.Fatalf("issues=%+v err=%v", issues, err)
	}
}

func TestParseIssuesReadsEveryPage(t *testing.T) {
	for _, data := range []string{`[{"number":1}]` + "\n" + `[{"number":2}]`, `[{"number":1},{"number":2}]`} {
		issues, err := ParseIssues([]byte(data))
		if err != nil || len(issues) != 2 || issues[0].Number != 1 || issues[1].Number != 2 {
			t.Fatal(issues, err)
		}
	}
	for _, data := range []string{`{"message":"Not Found"}`, `null`, "", `[] {`} {
		if _, err := ParseIssues([]byte(data)); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
}

const rankField = `{"__typename":"ProjectV2Field","dataType":"NUMBER"}`

func rankPage(field, nodes, more string) string {
	return `{"data":{"repositoryOwner":{"projectV2":{"field":` + field + `,"items":{"pageInfo":{"hasNextPage":` + more + `},"nodes":[` + nodes + `]}}}}}`
}

const rankedNode = `{"content":{"__typename":"Issue","number":4,"repository":{"nameWithOwner":"Acme/App"}},"fieldValueByName":{"__typename":"ProjectV2ItemFieldNumberValue","number":0.5}}`

func TestGHAsksForProjectRanks(t *testing.T) {
	var got []string
	gh := GH{Run: func(_ context.Context, args ...string) ([]byte, error) {
		got = args
		return []byte(rankPage(rankField, rankedNode, "false")), nil
	}}
	ranks, err := gh.ProjectRanks(context.Background(), Project{Owner: "acme", Number: 3}, "Rank", "acme/app")
	want := []string{"api", "graphql", "--paginate", "-f", "query=" + projectRanksQuery, "-f", "owner=acme", "-F", "number=3", "-f", "field=Rank"}
	if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(ranks, map[int]float64{4: 0.5}) {
		t.Fatal(got, ranks, err)
	}
}

func TestParseProjectRanksKeepsIssuesOfTheRepository(t *testing.T) {
	other := `,{"content":{"__typename":"Issue","number":5,"repository":{"nameWithOwner":"acme/other"}},"fieldValueByName":{"__typename":"ProjectV2ItemFieldNumberValue","number":1}},
 {"content":{"__typename":"PullRequest","number":6},"fieldValueByName":{"__typename":"ProjectV2ItemFieldNumberValue","number":3}},
 {"content":{"__typename":"DraftIssue"},"fieldValueByName":null},
 {"content":null,"fieldValueByName":null},
 {"content":{"__typename":"Issue","number":7,"repository":{"nameWithOwner":"acme/app"}},"fieldValueByName":null}`
	data := rankPage(rankField, rankedNode+other, "true") + "\n" + rankPage(rankField, strings.ReplaceAll(rankedNode, `"number":4`, `"number":2`), "false")
	ranks, err := ParseProjectRanks("acme/app", []byte(data))
	if err != nil || !reflect.DeepEqual(ranks, map[int]float64{4: 0.5, 2: 0.5}) {
		t.Fatal(ranks, err)
	}
}

func TestParseProjectRanksRejectsBadFieldsAndIncompleteReads(t *testing.T) {
	for data, message := range map[string]string{
		`{"data":{"repositoryOwner":null}}`:                                        "not found",
		`{"data":{"repositoryOwner":{"projectV2":null}}}`:                          "not found",
		rankPage("null", "", "false"):                                              "no field",
		rankPage(`{"__typename":"ProjectV2SingleSelectField"}`, "", "false"):       "not a number field",
		rankPage(`{"__typename":"ProjectV2Field","dataType":"TEXT"}`, "", "false"): "not a number field",
		`{"errors":[{"message":"permission denied"}]}`:                             "permission denied",
		rankPage(rankField, "", "true"):                                            "EOF",
		`{"data":{"repositoryOwner":{"projectV2":{"field":` + rankField + `}}}}`:   "missing project items",
	} {
		if _, err := ParseProjectRanks("acme/app", []byte(data)); err == nil || !strings.Contains(err.Error(), message) {
			t.Fatalf("%s: %v", data, err)
		}
	}
}

func TestParseProject(t *testing.T) {
	project, err := ParseProject("acme/3")
	if err != nil || project != (Project{Owner: "acme", Number: 3}) {
		t.Fatal(project, err)
	}
	for _, value := range []string{"acme", "acme/x", "acme/0", "/3", "acme/3/4"} {
		if _, err := ParseProject(value); err == nil {
			t.Fatal(value)
		}
	}
}
