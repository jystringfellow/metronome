package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func publishPage(status, state, labels, assignees string) string {
	return fmt.Sprintf(`{"data":{"repositoryOwner":{"projectV2":{"id":"P1","field":{"__typename":"ProjectV2SingleSelectField","id":"F1","options":[{"id":"ready","name":"Ready"},{"id":"progress","name":"In progress"},{"id":"review","name":"In review"},{"id":"done","name":"Done"}]},"items":{"pageInfo":{"hasNextPage":false},"nodes":[{"id":"PV1","content":{"__typename":"Issue","number":7,"state":%q,"body":"Metronome: MET-18","repository":{"nameWithOwner":"acme/app"},"assignees":{"nodes":%s},"labels":{"nodes":%s}},"fieldValueByName":{"name":%q}}]}}}}}`, state, assignees, labels, status)
}

type ghExchange struct {
	args     []string
	response string
	err      error
}

func exchanges(t *testing.T, expected []ghExchange) (GH, func()) {
	t.Helper()
	index := 0
	run := func(_ context.Context, args ...string) ([]byte, error) {
		t.Helper()
		if index >= len(expected) {
			t.Fatalf("unexpected gh args: %q", args)
		}
		call := expected[index]
		index++
		if !reflect.DeepEqual(args, call.args) {
			t.Fatalf("call %d: got %q, want %q", index, args, call.args)
		}
		return []byte(call.response), call.err
	}
	done := func() {
		t.Helper()
		if index != len(expected) {
			t.Fatalf("made %d of %d calls", index, len(expected))
		}
	}
	return GH{Run: run}, done
}

func projectCall(response string) ghExchange {
	return ghExchange{args: []string{"api", "graphql", "--paginate", "-f", "query=" + projectPublishQuery, "-f", "owner=acme", "-F", "number=3", "-f", "field=Status"}, response: response}
}

func statusCall(option string) ghExchange {
	return ghExchange{args: []string{"api", "graphql", "-f", "query=" + setStatusMutation, "-f", "project=P1", "-f", "item=PV1", "-f", "field=F1", "-f", "option=" + option}, response: `{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"PV1"}}}}`}
}

func apiCall(args ...string) ghExchange {
	return ghExchange{args: append([]string{"api"}, args...), response: `{}`}
}

func publisherOptions(t *testing.T) PublishOptions {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("null"), 0600); err != nil {
		t.Fatal(err)
	}
	return PublishOptions{Project: Project{Owner: "acme", Number: 3}, Field: "Status", Instance: "laptop-a", Assignee: "pat", StateFile: path, Mapping: DefaultMapping()}
}

const claimedRecord = `{"seq":1,"item_id":"I1","slice_id":"I1.1","ref":"acme/app#7","transition":"claimed"}` + "\n"
const prRecord = `{"seq":2,"item_id":"I1","slice_id":"I1.1","ref":"MET-18","transition":"pr_open","pr":{"repo":"acme/app","number":12,"head":"abc"}}` + "\n"
const reviewRecord = `{"seq":3,"item_id":"I1","slice_id":"I1.1","ref":"acme/app#7","transition":"in_review"}` + "\n"
const doneRecord = `{"seq":4,"item_id":"I1","ref":"acme/app#7","transition":"done"}` + "\n"

func TestPublisherAppAssigneeHint(t *testing.T) {
	options := publisherOptions(t)
	options.Assignee = ""
	gh, done := exchanges(t, []ghExchange{projectCall(publishPage("Ready", "OPEN", "[]", "[]")), {args: []string{"api", "user", "--jq", ".login"}, err: errors.New("login unavailable")}})
	var out, notes bytes.Buffer
	if err := gh.Publish(context.Background(), options, strings.NewReader(""), &out, &notes); err == nil || !strings.Contains(err.Error(), "--assignee LOGIN") {
		t.Fatal("missing App assignee hint", err)
	}
	done()
}

func TestPublisherAdoptedSourceIssue(t *testing.T) {
	options := publisherOptions(t)
	label := "metronome:claimed:laptop-a"
	endpoint := "repos/acme/app/issues/7"
	gh, check := exchanges(t, []ghExchange{
		projectCall(publishPage("Ready", "OPEN", "[]", "[]")),
		{args: []string{"label", "create", label, "--repo", "acme/app", "--force", "--color", "BFDADC", "--description", "Claimed by Metronome instance laptop-a"}},
		apiCall("-X", "POST", endpoint+"/labels", "-f", "labels[]="+label),
		{args: []string{"api", "--paginate", endpoint + "/comments"}, response: `[]`},
		apiCall(endpoint+"/comments", "-f", "body=Metronome slice I1.1: https://github.com/acme/app/pull/12\n\n<!-- metronome:pr acme/app#12 -->"),
		statusCall("progress"),
	})
	input := strings.Replace(prRecord, "pr_open", "adopted", 1)
	if err := gh.Publish(t.Context(), options, strings.NewReader(input), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	check()
	state, err := loadPublishState(options.StateFile)
	if err != nil || state.Issues["acme/app#7"].Assigned != "" || state.Issues["acme/app#7"].Status != "In progress" {
		t.Fatal(state, err)
	}
}

func TestPublisherSkipsDirectAdoptionAndContinues(t *testing.T) {
	options := publisherOptions(t)
	direct := `{"seq":1,"item_id":"I2","slice_id":"I2.1","ref":"acme/app#12","transition":"adopted","pr":{"repo":"acme/app","number":12,"head":"abc"}}` + "\n" + `{"seq":1,"item_id":"I2","ref":"acme/app#12","transition":"executing"}` + "\n"
	gh, check := exchanges(t, []ghExchange{
		projectCall(publishPage("Ready", "OPEN", "[]", "[]")),
		{args: []string{"api", "repos/acme/app/issues/12"}, response: `{"number":12,"state":"open","pull_request":{"url":"https://example.com/pr/12"}}`},
		statusCall("review"),
	})
	var notes bytes.Buffer
	if err := gh.Publish(t.Context(), options, strings.NewReader(direct+reviewRecord), &bytes.Buffer{}, &notes); err != nil {
		t.Fatal(err)
	}
	check()
	if strings.Count(notes.String(), "pull request, not a source issue; skipped") != 2 {
		t.Fatal(notes.String())
	}
	state, err := loadPublishState(options.StateFile)
	if err != nil || state.Seq != 3 || len(state.Issues) != 1 || state.Issues["acme/app#7"].Status != "In review" {
		t.Fatal(state, err)
	}
	gh, check = exchanges(t, []ghExchange{
		projectCall(publishPage("In review", "OPEN", "[]", "[]")),
		{args: []string{"api", "repos/acme/app/issues/12"}, response: `{"number":12,"state":"closed","pull_request":{"url":"https://example.com/pr/12"}}`},
	})
	if err := gh.Publish(t.Context(), options, strings.NewReader(`{"seq":4,"item_id":"I2","ref":"acme/app#12","transition":"done"}`+"\n"), &bytes.Buffer{}, &notes); err != nil {
		t.Fatal(err)
	}
	check()
	state, err = loadPublishState(options.StateFile)
	if err != nil || state.Seq != 4 {
		t.Fatal(state, err)
	}
}

func TestPublisherGithubLifecycleAndReplay(t *testing.T) {
	options := publisherOptions(t)
	label := "metronome:claimed:laptop-a"
	endpoint := "repos/acme/app/issues/7"
	gh, check := exchanges(t, []ghExchange{
		projectCall(publishPage("Ready", "OPEN", "[]", "[]")),
		{args: []string{"label", "create", label, "--repo", "acme/app", "--force", "--color", "BFDADC", "--description", "Claimed by Metronome instance laptop-a"}},
		apiCall("-X", "POST", endpoint+"/labels", "-f", "labels[]="+label),
		apiCall("-X", "POST", endpoint+"/assignees", "-f", "assignees[]=pat"),
		statusCall("progress"),
		{args: []string{"api", "--paginate", endpoint + "/comments"}, response: `[]`},
		apiCall(endpoint+"/comments", "-f", "body=Metronome slice I1.1: https://github.com/acme/app/pull/12\n\n<!-- metronome:pr acme/app#12 -->"),
		statusCall("review"), statusCall("done"),
		apiCall("-X", "PATCH", endpoint, "-f", "state=closed", "-f", "state_reason=completed"),
		apiCall("-X", "DELETE", endpoint+"/labels/"+label),
		apiCall("-X", "DELETE", endpoint+"/assignees", "-f", "assignees[]=pat"),
	})
	var out, notes bytes.Buffer
	input := claimedRecord + prRecord + reviewRecord + doneRecord
	if err := gh.Publish(t.Context(), options, strings.NewReader(input), &out, &notes); err != nil {
		t.Fatal(err)
	}
	check()
	state, err := loadPublishState(options.StateFile)
	if err != nil || len(state.Issues) != 0 || state.Seq != 4 {
		t.Fatal(state, err)
	}
	gh, check = exchanges(t, []ghExchange{projectCall(publishPage("Done", "CLOSED", "[]", "[]"))})
	if err := gh.Publish(t.Context(), options, strings.NewReader(input), &out, &notes); err != nil {
		t.Fatal(err)
	}
	check()
	if notes.Len() != 0 {
		t.Fatal(notes.String())
	}
}

func TestPublisherProtectsHumanStatusAndRetries(t *testing.T) {
	options := publisherOptions(t)
	endpoint := "repos/acme/app/issues/7"
	label := `[{"name":"metronome:claimed:laptop-a"}]`
	assignee := `[{"login":"pat"}]`
	gh, check := exchanges(t, []ghExchange{projectCall(publishPage("Ready", "OPEN", label, assignee)), statusCall("progress"), {args: []string{"api", "--paginate", endpoint + "/comments"}, err: errors.New("failed read")}})
	var out, notes bytes.Buffer
	if err := gh.Publish(t.Context(), options, strings.NewReader(claimedRecord+prRecord), &out, &notes); err == nil {
		t.Fatal("expected failed read")
	}
	check()
	state, err := loadPublishState(options.StateFile)
	if err != nil || state.Seq != 1 || state.Issues["acme/app#7"].Status != "In progress" || state.Issues["acme/app#7"].Assigned != "" {
		t.Fatal(state, err)
	}
	gh, check = exchanges(t, []ghExchange{
		projectCall(publishPage("Blocked", "OPEN", label, assignee)),
		{args: []string{"api", "--paginate", endpoint + "/comments"}, response: `[{"body":"<!-- metronome:pr acme/app#12 -->"}]`},
		apiCall("-X", "PATCH", endpoint, "-f", "state=closed", "-f", "state_reason=completed"),
		apiCall("-X", "DELETE", endpoint+"/labels/metronome:claimed:laptop-a"),
	})
	if err := gh.Publish(t.Context(), options, strings.NewReader(claimedRecord+prRecord+reviewRecord+doneRecord), &out, &notes); err != nil {
		t.Fatal(err)
	}
	check()
	if !strings.Contains(notes.String(), "changed since Metronome set In progress; left alone") {
		t.Fatal(notes.String())
	}
}

func TestPublisherProjectValidationAndMapping(t *testing.T) {
	page := publishPage("Ready", "OPEN", "[]", "[]")
	board, err := parsePublishProject([]byte(page))
	if err != nil || board.Field != "F1" || board.Options["In progress"] != "progress" || len(board.Issues) != 1 || board.Issues[0].Body != "Metronome: MET-18" {
		t.Fatal(board, err)
	}
	for _, bad := range []string{strings.Replace(page, "ProjectV2SingleSelectField", "ProjectV2Field", 1), strings.Replace(page, `"hasNextPage":false`, `"hasNextPage":true`, 1), `{"errors":[{"message":"denied"}]}`} {
		if _, err := parsePublishProject([]byte(bad)); err == nil {
			t.Fatal(bad)
		}
	}
	options := publisherOptions(t)
	options.Mapping["claimed"] = "Missing"
	gh, check := exchanges(t, []ghExchange{projectCall(page)})
	if err := gh.Publish(t.Context(), options, strings.NewReader(claimedRecord), &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected missing option error")
	}
	check()
	if !reflect.DeepEqual(DefaultMapping(), map[string]string{"planning": "Ready", "ready": "Ready", "claimed": "In progress", "adopted": "In progress", "submitted": "In progress", "pr_open": "In progress", "rework": "In progress", "in_review": "In review", "approved": "In review", "done": "Done"}) {
		t.Fatal(DefaultMapping())
	}
}

func TestPublisherAdoptsMatchingStatus(t *testing.T) {
	options := publisherOptions(t)
	gh, check := exchanges(t, []ghExchange{projectCall(publishPage("In review", "OPEN", "[]", "[]"))})
	if err := gh.Publish(t.Context(), options, strings.NewReader(reviewRecord), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	check()
	data, err := os.ReadFile(options.StateFile)
	var state publishState
	if err != nil || json.Unmarshal(data, &state) != nil || state.Issues["acme/app#7"].Status != "In review" {
		t.Fatal(string(data), err)
	}
}

func TestPublisherDoesNotAdoptAHumanEdit(t *testing.T) {
	options := publisherOptions(t)
	if err := os.WriteFile(options.StateFile, []byte(`{"issues":{"acme/app#7":{"status":"In progress"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	gh, check := exchanges(t, []ghExchange{projectCall(publishPage("In review", "OPEN", "[]", "[]")), apiCall("-X", "PATCH", "repos/acme/app/issues/7", "-f", "state=closed", "-f", "state_reason=completed")})
	var notes bytes.Buffer
	if err := gh.Publish(t.Context(), options, strings.NewReader(reviewRecord+doneRecord), &bytes.Buffer{}, &notes); err != nil {
		t.Fatal(err)
	}
	check()
	if strings.Count(notes.String(), "changed since Metronome set In progress") != 2 {
		t.Fatal(notes.String())
	}
}

func TestPublisherDirectIssueOutsideProject(t *testing.T) {
	options := publisherOptions(t)
	options.Assignee = ""
	gh, check := exchanges(t, []ghExchange{projectCall(publishPage("Ready", "OPEN", "[]", "[]")), {args: []string{"api", "user", "--jq", ".login"}, response: "pat\n"}, {args: []string{"api", "repos/acme/app/issues/9"}, response: `{"number":9,"state":"open","assignees":[],"labels":[]}`}, apiCall("-X", "PATCH", "repos/acme/app/issues/9", "-f", "state=closed", "-f", "state_reason=completed")})
	var notes bytes.Buffer
	if err := gh.Publish(t.Context(), options, strings.NewReader(strings.ReplaceAll(doneRecord, "#7", "#9")), &bytes.Buffer{}, &notes); err != nil {
		t.Fatal(err)
	}
	check()
	if !strings.Contains(notes.String(), "not on project; Status skipped") {
		t.Fatal(notes.String())
	}
}

func TestPublisherDroppedClearsClaimWithoutClosing(t *testing.T) {
	options := publisherOptions(t)
	label := "metronome:claimed:laptop-a"
	endpoint := "repos/acme/app/issues/7"
	gh, check := exchanges(t, []ghExchange{
		projectCall(publishPage("Ready", "OPEN", "[]", `[{"login":"rev"}]`)),
		{args: []string{"label", "create", label, "--repo", "acme/app", "--force", "--color", "BFDADC", "--description", "Claimed by Metronome instance laptop-a"}},
		apiCall("-X", "POST", endpoint+"/labels", "-f", "labels[]="+label),
		apiCall("-X", "POST", endpoint+"/assignees", "-f", "assignees[]=pat"),
		statusCall("progress"),
	})
	var out, notes bytes.Buffer
	if err := gh.Publish(t.Context(), options, strings.NewReader(claimedRecord), &out, &notes); err != nil {
		t.Fatal(err)
	}
	check()
	gh, check = exchanges(t, []ghExchange{
		projectCall(publishPage("In progress", "OPEN", `[{"name":"metronome:claimed:laptop-a"},{"name":"metronome:claimed:laptop-b"}]`, `[{"login":"rev"},{"login":"pat"}]`)),
		apiCall("-X", "DELETE", endpoint+"/labels/"+label),
		apiCall("-X", "DELETE", endpoint+"/assignees", "-f", "assignees[]=pat"),
	})
	dropped := strings.ReplaceAll(doneRecord, `"done"`, `"dropped"`)
	if err := gh.Publish(t.Context(), options, strings.NewReader(dropped), &out, &notes); err != nil {
		t.Fatal(err)
	}
	check()
	state, err := loadPublishState(options.StateFile)
	if err != nil || state.Seq != 4 || len(state.Issues) != 0 {
		t.Fatal(state, err)
	}
	if notes.Len() != 0 || strings.Contains(out.String(), "closed") {
		t.Fatal(out.String(), notes.String())
	}
}

func TestPublisherSkipsUnmatchedAndAmbiguousBodyRefs(t *testing.T) {
	options := publisherOptions(t)
	page := publishPage("Ready", "OPEN", "[]", "[]")
	first := strings.Replace(page, `"hasNextPage":false`, `"hasNextPage":true`, 1)
	second := strings.ReplaceAll(strings.ReplaceAll(page, `"number":7`, `"number":8`), `"PV1"`, `"PV2"`)
	gh, check := exchanges(t, []ghExchange{projectCall(first + "\n" + second), statusCall("review")})
	input := `{"seq":1,"item_id":"I2","ref":"MET-19","transition":"done"}` + "\n" + prRecord + reviewRecord
	var out, notes bytes.Buffer
	if err := gh.Publish(t.Context(), options, strings.NewReader(input), &out, &notes); err != nil {
		t.Fatal(err)
	}
	check()
	for _, ref := range []string{"MET-19", "MET-18"} {
		if !strings.Contains(notes.String(), ref+": no unique project issue; skipped") {
			t.Fatal(notes.String())
		}
	}
	state, err := loadPublishState(options.StateFile)
	if err != nil || state.Seq != 3 || len(state.Issues) != 1 || state.Issues["acme/app#7"].Status != "In review" {
		t.Fatal(state, err)
	}
}
