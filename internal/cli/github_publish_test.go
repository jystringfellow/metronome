package cli_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/jystringfellow/metronome/internal/cli"
	"github.com/jystringfellow/metronome/internal/github"
)

type publisherStub func(github.PublishOptions, io.Reader) error

func (p publisherStub) Publish(_ context.Context, options github.PublishOptions, input io.Reader, _, _ io.Writer) error {
	return p(options, input)
}

func TestGitHubPublisherFlagsAndInput(t *testing.T) {
	t.Setenv("METRONOME_PUBLISHER_STATE", "state.json")
	input := `{"transition":"done","ref":"MET-18"}`
	calls := 0
	stub := publisherStub(func(options github.PublishOptions, reader io.Reader) error {
		calls++
		data, err := io.ReadAll(reader)
		if err != nil || string(data) != input || options.Project.Owner != "acme" || options.Project.Number != 3 || options.Instance != "laptop-a" || options.Field != "Workflow" || options.StateFile != "state.json" || options.Assignee != "pat" || options.Mapping["claimed"] != "Working" || options.Mapping["submitted"] != "" {
			t.Fatal(options, string(data), err)
		}
		return nil
	})
	var out, notes bytes.Buffer
	deps := cli.Deps{Projects: stub, Stdin: strings.NewReader(input)}
	args := []string{"github", "publish", "--project", "acme/3", "--instance", "laptop-a", "--status-field", "Workflow", "--assignee", "pat", "--map", "claimed=Working", "--map", "submitted="}
	if code := cli.RunWithDeps(t.Context(), args, &out, &notes, deps); code != 0 {
		t.Fatal(code, notes.String())
	}
	for _, args := range [][]string{{"github", "publish"}, {"github", "publish", "--project", "acme/3"}, {"github", "publish", "--project", "acme/3", "--instance", "laptop-a", "--map", "unknown=Done"}} {
		if code := cli.RunWithDeps(t.Context(), args, &out, &notes, deps); code != 2 {
			t.Fatal(args, code)
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
