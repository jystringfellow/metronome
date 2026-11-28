package github

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParsePRRefAndSourceChoice(t *testing.T) {
	for _, ref := range []string{"ACME/App#12", "https://github.com/ACME/App/pull/12", "https://github.com/ACME/App/pull/12/files"} {
		repo, number, err := ParsePRRef(ref)
		if err != nil || repo != "acme/app" || number != 12 {
			t.Fatal(ref, repo, number, err)
		}
	}
	for _, ref := range []string{"acme/..#12", "acme/app#0", "https://example.com/acme/app/pull/12"} {
		if _, _, err := ParsePRRef(ref); err == nil {
			t.Fatal(ref)
		}
	}
	draft := &PullRequest{Snapshot: Snapshot{State: PROpen, Draft: true}}
	ready := &PullRequest{Snapshot: Snapshot{State: PROpen}}
	if pr, err := ChooseSourcePR([]*PullRequest{draft, ready}); err != nil || pr != ready {
		t.Fatal(pr, err)
	}
	if pr, err := ChooseSourcePR([]*PullRequest{draft}); err != nil || pr != draft {
		t.Fatal(pr, err)
	}
	if _, err := ChooseSourcePR([]*PullRequest{ready, ready}); err == nil {
		t.Fatal("expected ambiguity")
	}
}

func TestParseAdoptablePR(t *testing.T) {
	pr, err := ParsePR("acme/app", []byte(`{"number":12,"state":"OPEN","headRefOid":"abc","headRefName":"feature-x","headRepository":{"name":"app"},"headRepositoryOwner":{"login":"acme"},"author":{"login":"pat"},"title":"Export","body":"Export data.\n## Acceptance criteria\n- CSV","labels":[{"name":"tier:2"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req, err := AdoptionFor(pr)
	if err != nil || req.Branch != "feature-x" || req.Author != "pat" || req.Tier == nil || *req.Tier != 2 || req.Intent != "Export data." || !reflect.DeepEqual(req.Criteria, []string{"CSV"}) {
		t.Fatal(req, err)
	}
	pr.CrossRepository = true
	if _, err := AdoptionFor(pr); err == nil || !strings.Contains(err.Error(), "fork") {
		t.Fatal(err)
	}
}

func TestIssueBacklogPRReferences(t *testing.T) {
	issues := []Issue{{Number: 1, Title: "Export", Body: "PR: https://github.com/ACME/app/pull/12\nExport records.\n## Acceptance criteria\n- CSV\nPR: acme/app#13"}}
	items, err := IssueBacklog("acme/app", "pat", issues, nil, "", map[int][]string{1: {"acme/app#12", "acme/app#14"}})
	if err != nil || len(items) != 1 || !reflect.DeepEqual(items[0].PRs, []string{"acme/app#12", "acme/app#13", "acme/app#14"}) || items[0].Intent != "Export records." || !reflect.DeepEqual(items[0].Slices[0].Criteria, []string{"CSV"}) {
		t.Fatal(items, err)
	}
	issues[0].Body = "PR: bad"
	if _, err := IssueBacklog("acme/app", "pat", issues, nil, "", nil); err == nil {
		t.Fatal("expected bad PR error")
	}
}

func TestLinkedPRsRejectIncompleteResponses(t *testing.T) {
	for _, data := range []string{`{"errors":[{"message":"denied"}]}`, `{"data":{"repository":{"issues":{"nodes":[]}}}}`, `{"data":{"repository":{"issues":{"pageInfo":{"hasNextPage":false},"nodes":[{"number":1,"closedByPullRequestsReferences":{"totalCount":11,"nodes":[]}}]}}}}`} {
		if _, err := ParseLinkedPRs([]byte(data)); err == nil {
			t.Fatal(data)
		}
	}
}

func TestGHPRAndLabelledPRArguments(t *testing.T) {
	view := []string{"pr", "view", "12", "--repo", "acme/app", "--json", prFields + ",title,body,author,headRefName,headRepository,headRepositoryOwner,isCrossRepository,labels,url"}
	list := []string{"pr", "list", "--repo", "acme/app", "--label", "adopt", "--state", "open", "--limit", "1000", "--json", "number,isCrossRepository,labels"}
	gh, check := exchanges(t, []ghExchange{
		{args: view, response: `{"number":12,"state":"OPEN","headRefOid":"abc","headRefName":"feature-x","headRepository":{"name":"app"},"headRepositoryOwner":{"login":"acme"},"author":{"login":"pat"},"title":"Export"}`},
		{args: view, err: errors.New("GraphQL: Could not resolve to a PullRequest with the number of 12.")},
		{args: list, response: `[{"number":12,"isCrossRepository":true,"labels":[{"name":"adopt"}]}]`},
		{args: list, response: "[" + strings.Repeat(`{"number":12},`, 999) + `{"number":12}]`},
	})
	pr, err := gh.PR(t.Context(), "acme/app", 12)
	if err != nil || pr.Number != 12 || pr.HeadRepo != "acme/app" {
		t.Fatal(pr, err)
	}
	if pr, err = gh.PR(t.Context(), "acme/app", 12); pr != nil || err != nil {
		t.Fatal(pr, err)
	}
	labelled, err := gh.LabelledPRs(t.Context(), "acme/app", "adopt")
	if err != nil || !reflect.DeepEqual(labelled, []LabelledPR{{Number: 12, CrossRepository: true, Labels: []string{"adopt"}}}) {
		t.Fatal(labelled, err)
	}
	if _, err = gh.LabelledPRs(t.Context(), "acme/app", "adopt"); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatal(err)
	}
	check()
}

func TestAdoptionForRefusals(t *testing.T) {
	pr, err := ParsePR("acme/app", []byte(`{"number":12,"state":"OPEN","headRefOid":"abc","headRefName":"feature-x","headRepository":null,"headRepositoryOwner":{"login":"acme"},"author":{"login":"pat"},"title":"Export"}`))
	if err != nil || pr.HeadRepo != "" {
		t.Fatal(pr, err)
	}
	if _, err := AdoptionFor(pr); err == nil || !strings.Contains(err.Error(), "fork") {
		t.Fatal(err)
	}
	pr.HeadRepo = "acme/app"
	for _, state := range []PRState{PRClosed, PRMerged} {
		pr.State = state
		if _, err := AdoptionFor(pr); err == nil || !strings.Contains(err.Error(), "only open PRs") {
			t.Fatal(err)
		}
	}
	if _, err := ChooseSourcePR([]*PullRequest{pr, nil}); err == nil || !strings.Contains(err.Error(), "none of its PRs is open") {
		t.Fatal(err)
	}
	pr.State = PROpen
	pr.Author = ""
	pr.Draft = true
	if _, err := AdoptionFor(pr); err == nil || !strings.Contains(err.Error(), "no author") {
		t.Fatal(err)
	}
	other := *pr
	other.Number++
	if _, err := ChooseSourcePR([]*PullRequest{pr, &other}); err == nil || !strings.Contains(err.Error(), "a person must decide") {
		t.Fatal(err)
	}
}

func TestGHLinkedPRsPages(t *testing.T) {
	first := `{"data":{"repository":{"issues":{"pageInfo":{"hasNextPage":true,"endCursor":"cursor"},"nodes":[{"number":7,"closedByPullRequestsReferences":{"totalCount":1,"nodes":[{"number":12,"repository":{"nameWithOwner":"ACME/App"}}]}}]}}}}`
	second := `{"data":{"repository":{"issues":{"pageInfo":{"hasNextPage":false},"nodes":[{"number":8,"closedByPullRequestsReferences":{"totalCount":1,"nodes":[{"number":13,"repository":{"nameWithOwner":"acme/app"}}]}}]}}}}`
	gh, check := exchanges(t, []ghExchange{{args: []string{"api", "graphql", "--paginate", "-f", "query=" + linkedPRsQuery, "-f", "owner=acme", "-f", "repo=app", "-f", "label=metronome"}, response: first + "\n" + second}})
	refs, err := gh.LinkedPRs(t.Context(), "acme/app", "metronome")
	if err != nil || !reflect.DeepEqual(refs, map[int][]string{7: {"acme/app#12"}, 8: {"acme/app#13"}}) {
		t.Fatal(refs, err)
	}
	check()
	if _, err := ParseLinkedPRs([]byte(first)); err == nil {
		t.Fatal("accepted missing second page")
	}
}
