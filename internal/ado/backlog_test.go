package ado

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestBacklogMapping(t *testing.T) {
	items, err := ParseWorkItems([]byte(batchFixture))
	if err != nil {
		t.Fatal(err)
	}
	items[0].Description += `<p>Repo: acme/app</p>`
	items[0].Predecessors = append(items[0].Predecessors, 3, 99)
	items = append(items, WorkItem{ID: 3, State: "Done", Tags: []string{"tier:bad"}}, WorkItem{ID: 4, State: "Removed"}, WorkItem{ID: 5, Title: "Committed", State: "Committed"}, WorkItem{ID: 6, Title: "Ready", State: "To Do", Rank: items[0].Rank, AcceptanceCriteria: "<p>Repo: acme/app</p><p>Check one</p>"})
	result, err := Backlog(items, "pat", strings.Split(DefaultReadyStates, ","))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 4 {
		t.Fatal(result)
	}
	first, second, third, last := result.Items[0], result.Items[1], result.Items[2], result.Items[3]
	if first.Ref != "AB#1" || first.Intent != `"Export" <records>` || first.Owner != "pat" || first.ClaimedBy != "" || *first.PredictedTier != 0 || !reflect.DeepEqual(first.Labels, []string{"export"}) || first.Slices[0].Repo != "acme/app" || !reflect.DeepEqual(first.Slices[0].Criteria, []string{"CSV"}) {
		t.Fatal(first)
	}
	if second.Ref != "AB#6" || !reflect.DeepEqual(second.Slices[0].Criteria, []string{"Check one"}) || third.Ref != "AB#2" || third.Owner != "rev@example.com" || third.ClaimedBy != third.Owner || len(third.Slices) != 0 || last.Ref != "AB#5" || last.ClaimedBy != "state Committed" {
		t.Fatal(second, third, last)
	}
	notes := strings.Join(result.Notes, "\n")
	for _, want := range []string{"AB#3 is Done", "AB#4 is Removed", "AB#1 waits on AB#2 (in this result)", "AB#1 waits on AB#99, which is outside"} {
		if !strings.Contains(notes, want) {
			t.Fatal(notes)
		}
	}
	if strings.Contains(notes, "waits on AB#3") || !reflect.DeepEqual(result.Predecessors["AB#1"], []string{"AB#2", "AB#3", "AB#99"}) {
		t.Fatal(result)
	}
	result, err = Backlog(items, "pat", []string{"committed", "done"})
	if err != nil || len(result.Items) != 4 || result.Items[3].ClaimedBy != "" || result.Items[0].ClaimedBy != "state New" {
		t.Fatal(result, err)
	}
}

func TestBacklogLiteralAnglesAndRepo(t *testing.T) {
	items := []WorkItem{{
		ID: 1, Title: "Check values", State: "New",
		Description:        "Check x < y\nRepo: acme/app",
		AcceptanceCriteria: "<ul><li>a < b</li><li>a<b</li></ul>",
	}}
	result, err := Backlog(items, "pat", []string{"New"})
	if err != nil || len(result.Items) != 1 {
		t.Fatal(result, err)
	}
	got := result.Items[0]
	if got.Intent != "Check x < y" || len(got.Slices) != 1 || got.Slices[0].Repo != "acme/app" || !reflect.DeepEqual(got.Slices[0].Criteria, []string{"a < b", "a<b"}) {
		t.Fatal(got)
	}
}

func TestBacklogPRReferences(t *testing.T) {
	work := WorkItem{ID: 1, Title: "Export", State: "New", Description: "<p>PR: https://github.com/acme/app/pull/12</p><p>Export records</p>", AcceptanceCriteria: "<p>PR: acme/app#13</p><p>CSV</p>", LinkedPRs: 1}
	result, err := Backlog([]WorkItem{work}, "pat", []string{"New"})
	if err != nil || len(result.Items) != 1 {
		t.Fatal(result, err)
	}
	item := result.Items[0]
	if !reflect.DeepEqual(item.PRs, []string{"acme/app#12", "acme/app#13"}) || item.Intent != "Export records" || item.Slices[0].Repo != "acme/app" || !reflect.DeepEqual(item.Slices[0].Criteria, []string{"CSV"}) || item.ClaimedBy != "" || !strings.Contains(strings.Join(result.Notes, " "), "cannot resolve") {
		t.Fatal(result)
	}
	work.Description = "Export records"
	work.AcceptanceCriteria = "CSV"
	result, err = Backlog([]WorkItem{work}, "pat", []string{"New"})
	if err != nil || len(result.Items) != 1 || result.Items[0].ClaimedBy != "linked pull request" || !strings.Contains(strings.Join(result.Notes, " "), "add a PR:") {
		t.Fatal(result, err)
	}
	work.Description = "PR: bad"
	if _, err := Backlog([]WorkItem{work}, "pat", []string{"New"}); err == nil {
		t.Fatal("expected invalid PR")
	}
}

func TestBacklogErrors(t *testing.T) {
	items := []WorkItem{{ID: 1, State: "New", Tags: []string{"tier:9"}}, {ID: 2, Title: "Export", State: "New", Tags: []string{"tier:1", "tier:2"}, Description: "Repo: acme/app\nRepo: acme/other"}, {ID: 3, Title: "Import", State: "New", Description: "Repo: bad"}}
	_, err := Backlog(items, "", []string{"New"})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"has no Assigned To", "needs a title", "invalid tier label", "conflicting tier labels", "conflicting Repo:", "invalid Repo:"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatal(err)
		}
	}
}

func TestBacklogNormalisesTagsIntoLabels(t *testing.T) {
	tags := []string{"tier:1", "Shared  Library", "shared library", "C#", "metronome"}
	for i := 0; i < 30; i++ {
		tags = append(tags, fmt.Sprintf("tag %02d", i))
	}
	result, err := Backlog([]WorkItem{{ID: 7, Title: "Adopt", State: "New", Tags: tags, Description: "<p>Repo: acme/app</p>"}}, "pat", nil)
	if err != nil {
		t.Fatal(err)
	}
	labels := result.Items[0].Labels
	if len(labels) != 20 || labels[0] != "shared library" || labels[1] != "metronome" || slices.Contains(labels, "c#") {
		t.Fatalf("labels = %q", labels)
	}
}
