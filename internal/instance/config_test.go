package instance

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestConfig(t *testing.T) {
	cfg, err := Parse(strings.NewReader(`{"version":1,"actor":"pat","workstreams":[{"name":"example","limits":{"execute":0},"adopt_label":"adopt","intake":{"every":"2s","command":["cat","backlog.json"]},"publish":[{"name":"board","every":"1m","command":["cat"],"actor":"github"}]}],"github_poll":{"every":"3s","adopt_repos":["acme/app"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ResolveActors("fallback"); err != nil {
		t.Fatal(err)
	}
	ws := cfg.Workstreams[0]
	if ws.Name != "example" || ws.Limits.Plan != nil || ws.Limits.Execute == nil || *ws.Limits.Execute != 0 || *ws.AdoptLabel != "adopt" || ws.Intake.Actor != "pat" || ws.Intake.Interval() != 2*time.Second || ws.Intake.Command[1] != "backlog.json" || ws.Publish[0].BatchSize() != 100 || ws.Publish[0].Actor != "github" || cfg.GitHubPoll.Actor != "pat" || cfg.GitHubPoll.AdoptRepos[0] != "acme/app" {
		t.Fatalf("bad config: %+v", cfg)
	}
	data, err := os.ReadFile("../../docs/configuration.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, found := strings.Cut(string(data), "```json\n")
	if !found {
		t.Fatal("missing example")
	}
	example, _, found := strings.Cut(rest, "```")
	if !found {
		t.Fatal("unterminated example")
	}
	if _, err := Parse(strings.NewReader(example)); err != nil {
		t.Fatal(err)
	}
	minimal, err := Parse(strings.NewReader(`{"version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := minimal.ResolveActors(""); err == nil {
		t.Fatal("missing actor accepted")
	}
	if err := minimal.ResolveActors("pat"); err != nil || minimal.Actor != "pat" {
		t.Fatal(minimal, err)
	}
}

func TestInvalidConfig(t *testing.T) {
	for _, test := range []struct{ input, message string }{
		{`{}`, "version"}, {`{"version":2}`, "version"}, {`{"version":1,"token":"example"}`, "token"},
		{`{"version":1,"workstreams":[{"name":"example","intake":{"env":{}}}]}`, "env"},
		{`{"version":1,"workstreams":[{"name":"example"},{"name":"example"}]}`, "name"},
		{`{"version":1,"workstreams":[{"name":"Example"}]}`, "name"},
		{`{"version":1,"workstreams":[{"name":"example","limits":{"execute":-1}}]}`, "execute"},
		{`{"version":1,"github_poll":{}}`, "every"}, {`{"version":1,"github_poll":{"every":"0s"}}`, "every"},
		{`{"version":1,"github_poll":{"every":"-1m"}}`, "every"}, {`{"version":1,"github_poll":{"every":"soon"}}`, "every"},
		{`{"version":1,"workstreams":[{"name":"example","intake":{"every":"1s","command":[]}}]}`, "command"},
		{`{"version":1,"workstreams":[{"name":"example","intake":{"every":"1s","command":[""]}}]}`, "command"},
		{`{"version":1,"workstreams":[{"name":"example","publish":[{"name":"board","every":"1s","command":["cat"]},{"name":"board","every":"1s","command":["cat"]}]}]}`, "name"},
		{`{"version":1,"workstreams":[{"name":"example","publish":[{"name":"board","every":"1s","command":["cat"],"batch":0}]}]}`, "batch"},
		{`{"version":1,"workstreams":[{"name":"example","publish":[{"name":"board","every":"1s","command":["cat"],"batch":1001}]}]}`, "batch"},
		{`{"version":1,"github_poll":{"every":"1s","adopt_repos":["acme/.."]}}`, "adopt_repos"},
		{`{"version":1,"workstreams":[{"name":"example","adopt_label":"Ready"},{"name":"team","adopt_label":" ready "}]}`, "adopt_label"},
		{`{"version":1} {}`, "exactly one"}, {``, "EOF"},
	} {
		if _, err := Parse(strings.NewReader(test.input)); err == nil || !strings.Contains(err.Error(), test.message) {
			t.Errorf("%s: wanted %q, got %v", test.input, test.message, err)
		}
	}
}
