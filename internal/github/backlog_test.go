package github

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jystringfellow/metronome/internal/flow"
)

func TestIssueBacklogMapsAnIssue(t *testing.T) {
	issues, err := ParseIssues([]byte(issuePage))
	if err != nil {
		t.Fatal(err)
	}
	items, err := IssueBacklog("acme/app", "rev", issues[:1], nil, "")
	tier := 2
	want := []flow.BacklogItem{{Ref: "acme/app#4", Title: "Export", Intent: "Export records.", Owner: "pat", PredictedTier: &tier, Slices: []flow.SliceSpec{{Title: "Export", Repo: "acme/app", Criteria: []string{"CSV"}, Approvers: []string{}}}}}
	if err != nil || !reflect.DeepEqual(items, want) {
		t.Fatalf("%+v %v", items, err)
	}
	items, err = IssueBacklog("acme/app", "pat", []Issue{{Number: 2, Title: "Import", Body: "Metronome: MET-17"}}, nil, "")
	if err != nil || items[0].Ref != "MET-17" || items[0].Owner != "pat" || items[0].PredictedTier != nil {
		t.Fatal(items, err)
	}
}

func TestIssueTier(t *testing.T) {
	for _, test := range []struct {
		labels []string
		want   int
		bad    bool
	}{
		{[]string{"Tier: 0"}, 0, false}, {[]string{"tier:1", "tier:1"}, 1, false}, {[]string{"tier:7"}, 0, true}, {[]string{"tier:high"}, 0, true}, {[]string{"tier:1", "tier:2"}, 0, true},
	} {
		tier, err := TierLabel(test.labels)
		if test.bad {
			if err == nil {
				t.Fatal(test.labels)
			}
		} else if err != nil || tier == nil || *tier != test.want {
			t.Fatal(test.labels, tier, err)
		}
	}
}

func TestIssueBacklogForeignClaims(t *testing.T) {
	issues := []Issue{{Number: 7, Title: "Export", Labels: []string{"metronome:claimed:laptop-b", "METRONOME:CLAIMED:laptop-a", "metronome:claimed:hosted"}}}
	for _, test := range []struct{ instance, want string }{{"laptop-a", "hosted,laptop-b"}, {"", "hosted,laptop-a,laptop-b"}} {
		items, err := IssueBacklog("acme/app", "pat", issues, nil, test.instance)
		if err != nil || items[0].ClaimedBy != test.want {
			t.Fatal(items, err)
		}
	}
	issues[0].Labels = []string{"metronome:claimed:laptop-a"}
	items, err := IssueBacklog("acme/app", "pat", issues, nil, "laptop-a")
	if err != nil || items[0].ClaimedBy != "" {
		t.Fatal(items, err)
	}
}

func TestIssueBacklogOrdersByRankThenAge(t *testing.T) {
	older := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	issues := []Issue{{Number: 3, Title: "Three", CreatedAt: older}, {Number: 1, Title: "One", CreatedAt: newer}, {Number: 5, Title: "Five", CreatedAt: older}, {Number: 4, Title: "Four", CreatedAt: newer}, {Number: 2, Title: "Two", CreatedAt: older}}
	for _, test := range []struct {
		ranks map[int]float64
		want  []string
	}{
		{map[int]float64{3: 2, 1: 1, 5: 1}, []string{"acme/app#5", "acme/app#1", "acme/app#3", "acme/app#2", "acme/app#4"}},
		{nil, []string{"acme/app#2", "acme/app#3", "acme/app#5", "acme/app#1", "acme/app#4"}},
	} {
		items, err := IssueBacklog("acme/app", "pat", issues, test.ranks, "")
		var refs []string
		for _, item := range items {
			refs = append(refs, item.Ref)
		}
		if err != nil || !slices.Equal(refs, test.want) {
			t.Fatal(refs, err)
		}
	}
}

func TestIssueBacklogReportsAllBadIssues(t *testing.T) {
	issues := []Issue{{Number: 1, Title: "One"}, {Number: 2, Title: "Two", Body: "Metronome: acme/app#1", Labels: []string{"tier:9"}}}
	items, err := IssueBacklog("acme/app", "", issues, nil, "")
	if items != nil || err == nil {
		t.Fatal(items, err)
	}
	for _, message := range []string{"acme/app#1 has no assignee", "acme/app#2 has no assignee", "invalid tier label", "is the ref of acme/app#1 and acme/app#2"} {
		if !strings.Contains(err.Error(), message) {
			t.Fatal(err)
		}
	}
}
