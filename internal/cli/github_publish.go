package cli

import (
	"context"
	"os"
	"strings"

	"github.com/jystringfellow/metronome/internal/github"
)

func (a *app) githubPublish(ctx context.Context, args []string) error {
	fs := newFlags("github publish")
	project := fs.String("project", "", "")
	instance := fs.String("instance", "", "")
	field := fs.String("status-field", "Status", "")
	assignee := fs.String("assignee", "", "")
	mapping := github.DefaultMapping()
	fs.Func("map", "", func(value string) error {
		name, target, ok := strings.Cut(value, "=")
		if !ok || !github.KnownTransition(name) {
			return usagef("--map needs a known TRANSITION=OPTION")
		}
		mapping[name] = target
		return nil
	})
	if err := parse(fs, args, 0); err != nil {
		return err
	}
	selected, err := github.ParseProject(*project)
	if err != nil {
		return usagef("%v", err)
	}
	if !github.InstanceName.MatchString(*instance) {
		return usagef("--instance must be a lowercase instance name of 1 to 32 characters")
	}
	if strings.TrimSpace(*field) == "" {
		return usagef("--status-field cannot be empty")
	}
	return a.projects.Publish(ctx, github.PublishOptions{Project: selected, Field: *field, Instance: *instance, Assignee: strings.TrimSpace(*assignee), StateFile: os.Getenv("METRONOME_PUBLISHER_STATE"), Mapping: mapping}, a.stdin, a.out, a.errOut)
}
