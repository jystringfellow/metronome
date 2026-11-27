package cli

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/jystringfellow/metronome/internal/ado"
	"github.com/jystringfellow/metronome/internal/flow"
)

var queryID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (a *app) ado(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "items" {
		return usagef("usage: ado items --org URL --project NAME (--wiql FILE | --query ID) [--owner WHO] [--ready-states S,...]")
	}
	fs := newFlags("ado items")
	org := fs.String("org", "", "")
	project := fs.String("project", "", "")
	wiql := fs.String("wiql", "", "")
	query := fs.String("query", "", "")
	owner := fs.String("owner", "", "")
	states := fs.String("ready-states", ado.DefaultReadyStates, "")
	if err := parse(fs, args[1:], 0); err != nil {
		return err
	}
	parsed, err := url.Parse(strings.TrimSpace(*org))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.ForceQuery || parsed.RawQuery != "" || strings.Contains(*org, "#") {
		return usagef("--org must be an absolute HTTP(S) URL without query or fragment")
	}
	if strings.TrimSpace(*project) == "" {
		return usagef("--project must be nonempty")
	}
	var fileSet, querySet bool
	fs.Visit(func(flagValue *flag.Flag) {
		fileSet = fileSet || flagValue.Name == "wiql"
		querySet = querySet || flagValue.Name == "query"
	})
	if fileSet == querySet {
		return usagef("specify exactly one of --wiql or --query")
	}
	target := ado.Target{Org: strings.TrimRight(parsed.String(), "/"), Project: strings.TrimSpace(*project), QueryID: *query}
	if fileSet {
		data, err := os.ReadFile(*wiql)
		if err != nil {
			return usagef("read --wiql %q: %v", *wiql, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			return usagef("--wiql file %q is empty", *wiql)
		}
		target.WIQL = string(data)
	} else if !queryID.MatchString(*query) {
		return usagef("--query must be a GUID")
	}
	ready := strings.Split(*states, ",")
	for index, state := range ready {
		ready[index] = strings.TrimSpace(state)
		if ready[index] == "" {
			return usagef("--ready-states cannot contain empty entries")
		}
	}
	ids, err := a.boards.Query(ctx, target)
	if err != nil {
		return err
	}
	items, err := a.boards.WorkItems(ctx, target.Org, target.Project, ids)
	if err != nil {
		return err
	}
	result, err := ado.Backlog(items, strings.TrimSpace(*owner), ready)
	if err != nil {
		return err
	}
	for _, note := range result.Notes {
		fmt.Fprintln(a.errOut, note)
	}
	return writeJSON(a.out, struct {
		Items []flow.BacklogItem `json:"items"`
	}{result.Items})
}
