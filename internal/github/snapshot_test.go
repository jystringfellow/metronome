package github

import (
	"context"
	"slices"
	"testing"
)

const prList = `[
  {"number": 3, "headRefOid": "old", "isDraft": false, "state": "CLOSED", "updatedAt": "2026-11-26T10:00:00Z", "reviews": [], "statusCheckRollup": []},
  {"number": 7, "headRefOid": "aaa", "isDraft": true, "state": "OPEN", "updatedAt": "2026-11-26T09:00:00Z",
   "reviews": [
     {"id": "PRR_1", "author": {"login": "rev"}, "state": "APPROVED", "submittedAt": "2026-11-26T09:30:00Z", "commit": {"oid": "aaa"}},
     {"id": "", "author": {"login": "bot"}, "state": "COMMENTED", "submittedAt": "2026-11-26T09:31:00Z", "commit": {"oid": "aaa"}}
   ],
   "statusCheckRollup": [
     {"__typename": "CheckRun", "status": "COMPLETED", "conclusion": "SUCCESS"},
     {"__typename": "CheckRun", "status": "COMPLETED", "conclusion": "SKIPPED"},
     {"__typename": "StatusContext", "state": "SUCCESS"}
   ]}
]`

func TestParsePRListPrefersTheOpenPRAndKeepsReviewCommits(t *testing.T) {
	snap, err := ParsePRList("acme/app", []byte(prList))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Number != 7 || snap.HeadSHA != "aaa" || !snap.Draft || snap.State != PROpen || snap.HeadChecks != ChecksSuccess {
		t.Fatalf("snapshot = %+v", snap)
	}
	if len(snap.Reviews) != 2 || snap.Reviews[0].CommitSHA != "aaa" || snap.Reviews[0].Author != "rev" {
		t.Fatalf("reviews = %+v", snap.Reviews)
	}
	if snap.Reviews[1].ID == "" {
		t.Fatal("a review GitHub gives no ID still needs one, since request IDs are built from it")
	}
}

func TestParsePRListKeepsWhoMerged(t *testing.T) {
	data := `[{"number":1,"headRefOid":"h","state":"MERGED","updatedAt":"2026-11-26T09:00:00Z","mergedBy":{"login":"pat","is_bot":false},"reviews":[],"statusCheckRollup":[]}]`
	snap, err := ParsePRList("acme/app", []byte(data))
	if err != nil || snap.MergedBy != "pat" {
		t.Fatalf("snapshot = %+v, err = %v", snap, err)
	}
}

func TestParsePRListHeadAuthor(t *testing.T) {
	for commits, want := range map[string]string{
		`[{"oid":"old","authors":[{"login":"pat"}]},{"oid":"h","authors":[{}, {"login":"sam"},{"login":"rev"}]}]`: "sam",
		`[{"oid":"old","authors":[{"login":"pat"}]}]`:                                                             "",
		`[{"oid":"h","authors":[{}]}]`:                                                                            "",
	} {
		snap, err := ParsePRList("acme/app", []byte(`[{"number":7,"headRefOid":"h","state":"OPEN","commits":`+commits+`}]`))
		if err != nil || snap.HeadAuthor != want {
			t.Fatalf("snapshot = %+v, err = %v, want author %q", snap, err, want)
		}
	}
}

func TestParsePRListWithNoPRs(t *testing.T) {
	snap, err := ParsePRList("acme/app", []byte(`[]`))
	if err != nil || snap != nil {
		t.Fatalf("snapshot = %+v, err = %v", snap, err)
	}
}

func TestHeadChecksTakeTheWorstResult(t *testing.T) {
	cases := map[string]Checks{
		`[]`: ChecksNone,
		`[{"__typename":"CheckRun","name":"c1","status":"COMPLETED","conclusion":"SUCCESS"},{"__typename":"CheckRun","name":"c2","status":"IN_PROGRESS"}]`:    ChecksPending,
		`[{"__typename":"CheckRun","name":"c3","status":"COMPLETED","conclusion":"FAILURE"},{"__typename":"CheckRun","name":"c4","status":"IN_PROGRESS"}]`:    ChecksFailure,
		`[{"__typename":"CheckRun","name":"c5","status":"COMPLETED","conclusion":"CANCELLED"}]`:                                                               ChecksFailure,
		`[{"__typename":"CheckRun","name":"c6","status":"COMPLETED","conclusion":"NEUTRAL"},{"__typename":"StatusContext","context":"s7","state":"PENDING"}]`: ChecksPending,
		`[{"__typename":"StatusContext","context":"s8","state":"ERROR"}]`:                                                                                     ChecksFailure,
	}
	for rollup, want := range cases {
		data := `[{"number":1,"headRefOid":"h","state":"OPEN","updatedAt":"2026-11-26T09:00:00Z","reviews":[],"statusCheckRollup":` + rollup + `}]`
		snap, err := ParsePRList("acme/app", []byte(data))
		if err != nil {
			t.Fatal(err)
		}
		if snap.HeadChecks != want {
			t.Errorf("%s: checks = %s, want %s", rollup, snap.HeadChecks, want)
		}
	}
}

func TestGHAsksForThePRsOfOneBranch(t *testing.T) {
	var got []string
	g := GH{Run: func(_ context.Context, args ...string) ([]byte, error) {
		got = args
		return []byte(`[]`), nil
	}}
	if _, err := g.PRForBranch(context.Background(), "acme/app", "metronome/I1.1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"pr", "list", "--repo", "acme/app", "--head", "metronome/I1.1", "--state", "all", "--limit", "10", "--json", prFields}
	if !slices.Equal(got, want) {
		t.Fatalf("gh %v", got)
	}
}

func TestOnlyTheLatestRunOfEachCheckCounts(t *testing.T) {
	data := `[{"number":1,"headRefOid":"h","state":"OPEN","updatedAt":"2026-11-26T09:00:00Z","reviews":[],"statusCheckRollup":[
	  {"__typename":"CheckRun","workflowName":"ci","name":"test","status":"COMPLETED","conclusion":"CANCELLED","startedAt":"2026-11-26T09:00:00Z"},
	  {"__typename":"CheckRun","workflowName":"ci","name":"test","status":"COMPLETED","conclusion":"SUCCESS","startedAt":"2026-11-26T09:05:00Z"},
	  {"__typename":"CheckRun","workflowName":"lint","name":"test","status":"COMPLETED","conclusion":"SUCCESS","startedAt":"2026-11-26T09:00:00Z"}
	]}]`
	snap, err := ParsePRList("acme/app", []byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if snap.HeadChecks != ChecksSuccess {
		t.Fatalf("a cancelled run superseded by a passing one gives %s", snap.HeadChecks)
	}
}
