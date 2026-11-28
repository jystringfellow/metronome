package github

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jystringfellow/metronome/internal/flow"
)

type PublishOptions struct {
	Project                              Project
	Field, Instance, Assignee, StateFile string
	Mapping                              map[string]string
}

type ProjectBoard interface {
	Publish(ctx context.Context, options PublishOptions, input io.Reader, out, notes io.Writer) error
}

func DefaultMapping() map[string]string {
	return map[string]string{"planning": "Ready", "ready": "Ready", "claimed": "In progress", "adopted": "In progress", "submitted": "In progress", "pr_open": "In progress", "rework": "In progress", "in_review": "In review", "approved": "In review", "done": "Done"}
}

func KnownTransition(name string) bool {
	switch name {
	case "planning", "ready", "executing", "awaiting_release", "releasing", "done", "dropped", "claimed", "adopted", "submitted", "pr_open", "in_review", "approved", "merged", "rework", "stalled", "parked", "flagged", "asked", "answered", "ask_timed_out":
		return true
	}
	return false
}

type publishedIssue struct {
	Status   string   `json:"status,omitempty"`
	Assigned string   `json:"assigned,omitempty"`
	Linked   []string `json:"linked,omitempty"`
}

type publishState struct {
	Issues    map[string]*publishedIssue `json:"issues"`
	Seq       int64                      `json:"seq,omitempty"`
	Completed []string                   `json:"completed,omitempty"`
}

func loadPublishState(path string) (publishState, error) {
	var state publishState
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return state, err
		}
		if err := json.Unmarshal(data, &state); err != nil {
			return state, err
		}
	}
	if state.Issues == nil {
		state.Issues = map[string]*publishedIssue{}
	}
	return state, nil
}

func savePublishState(path string, state publishState) error {
	if path == "" {
		return nil
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(data) > flow.MaxPublisherState {
		return fmt.Errorf("publisher state too large")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".publish-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

var issueReference = regexp.MustCompile(`^([A-Za-z0-9-]+/[A-Za-z0-9_.-]+)#([1-9][0-9]*)$`)

func (g GH) resolveIssue(ctx context.Context, board *projectBoard, ref string) (*projectIssue, error) {
	if match := issueReference.FindStringSubmatch(ref); match != nil && !strings.HasSuffix(match[1], "/.") && !strings.HasSuffix(match[1], "/..") {
		for _, issue := range board.Issues {
			if strings.EqualFold(issue.ref(), ref) {
				return issue, nil
			}
		}
		number, err := strconv.Atoi(match[2])
		if err != nil {
			return nil, err
		}
		issue, err := g.getIssue(ctx, match[1], number)
		if err != nil {
			return nil, err
		}
		board.Issues = append(board.Issues, issue)
		return issue, nil
	}
	var found *projectIssue
	for _, issue := range board.Issues {
		if issue.ID != "" && bodyRef(issue.Body) == ref {
			if found != nil {
				return nil, nil
			}
			found = issue
		}
	}
	return found, nil
}

func includesFold(values []string, value string) bool {
	for _, entry := range values {
		if strings.EqualFold(entry, value) {
			return true
		}
	}
	return false
}

func (g GH) Publish(ctx context.Context, options PublishOptions, input io.Reader, out, notes io.Writer) error {
	state, err := loadPublishState(options.StateFile)
	if err != nil {
		return err
	}
	if options.StateFile == "" {
		fmt.Fprintln(notes, "publisher state is not persisted; use metronome publish")
	}
	if options.Mapping == nil {
		options.Mapping = DefaultMapping()
	}
	board, err := g.scanProject(ctx, options.Project, options.Field)
	if err != nil {
		return err
	}
	for _, target := range options.Mapping {
		if target != "" && board.Options[target] == "" {
			return fmt.Errorf("project field lacks option %q", target)
		}
	}
	if options.Assignee == "" {
		data, err := g.call(ctx, "api", "user", "--jq", ".login")
		if err != nil {
			return err
		}
		options.Assignee = strings.TrimSpace(string(data))
		if options.Assignee == "" {
			return fmt.Errorf("gh returned no login")
		}
	}
	created := map[string]bool{}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	save := func() error { return savePublishState(options.StateFile, state) }
	for scanner.Scan() {
		var record flow.Transition
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return fmt.Errorf("transition: %w", err)
		}
		if record.Ref == "" || record.Transition == "" || record.ItemID == "" || record.Seq <= 0 {
			return fmt.Errorf("transition needs seq, item_id, ref and transition")
		}
		if !KnownTransition(record.Transition) {
			continue
		}
		key := record.ItemID + "/" + record.SliceID + "/" + record.Transition
		if record.Seq > 0 && (record.Seq < state.Seq || record.Seq == state.Seq && slices.Contains(state.Completed, key)) {
			continue
		}
		issue, err := g.resolveIssue(ctx, &board, record.Ref)
		if err != nil {
			return err
		}
		if issue == nil {
			fmt.Fprintf(notes, "%s: no unique project issue; skipped\n", record.Ref)
		} else if issue.IsPullRequest {
			fmt.Fprintf(notes, "%s: pull request, not a source issue; skipped\n", record.Ref)
		} else {
			ref := strings.ToLower(issue.ref())
			entry := state.Issues[ref]
			if entry == nil {
				entry = &publishedIssue{}
				state.Issues[ref] = entry
			}
			if err := g.publishRecord(ctx, options, board, issue, entry, record, created, save, out, notes); err != nil {
				return err
			}
			if record.Transition == "done" || record.Transition == "dropped" {
				delete(state.Issues, ref)
			}
		}
		if record.Seq > state.Seq {
			state.Seq, state.Completed = record.Seq, nil
		}
		state.Completed = append(state.Completed, key)
		if err := save(); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func (g GH) publishRecord(ctx context.Context, options PublishOptions, board projectBoard, issue *projectIssue, entry *publishedIssue, record flow.Transition, created map[string]bool, save func() error, out, notes io.Writer) error {
	endpoint := issue.endpoint()
	label := claimPrefix + options.Instance
	if record.Transition == "claimed" || record.Transition == "adopted" {
		if !includesFold(issue.Labels, label) {
			if !created[issue.Repo] {
				if _, err := g.call(ctx, "label", "create", label, "--repo", issue.Repo, "--force", "--color", "BFDADC", "--description", "Claimed by Metronome instance "+options.Instance); err != nil {
					return err
				}
				created[issue.Repo] = true
			}
			if _, err := g.call(ctx, "api", "-X", "POST", endpoint+"/labels", "-f", "labels[]="+label); err != nil {
				return err
			}
			issue.Labels = append(issue.Labels, label)
			fmt.Fprintf(out, "claimed %s\n", issue.ref())
		}
		if record.Transition == "claimed" && !includesFold(issue.Assignees, options.Assignee) {
			if _, err := g.call(ctx, "api", "-X", "POST", endpoint+"/assignees", "-f", "assignees[]="+options.Assignee); err != nil {
				return err
			}
			issue.Assignees = append(issue.Assignees, options.Assignee)
			entry.Assigned = options.Assignee
			if err := save(); err != nil {
				return err
			}
		}
	}
	if record.PR != nil && record.PR.Number > 0 {
		pr := fmt.Sprintf("%s#%d", record.PR.Repo, record.PR.Number)
		if !slices.Contains(entry.Linked, pr) {
			marker := "<!-- metronome:pr " + pr + " -->"
			linked, err := g.hasComment(ctx, endpoint, marker)
			if err != nil {
				return err
			}
			if !linked {
				body := fmt.Sprintf("Metronome slice %s: https://github.com/%s/pull/%d\n\n%s", record.SliceID, record.PR.Repo, record.PR.Number, marker)
				if _, err := g.call(ctx, "api", endpoint+"/comments", "-f", "body="+body); err != nil {
					return err
				}
				fmt.Fprintf(out, "commented %s %s\n", issue.ref(), pr)
			}
			entry.Linked = append(entry.Linked, pr)
			if err := save(); err != nil {
				return err
			}
		}
	}
	if target := options.Mapping[record.Transition]; target != "" {
		switch {
		case issue.ID == "":
			fmt.Fprintf(notes, "%s: not on project; Status skipped\n", issue.ref())
		case entry.Status != "" && issue.Status != entry.Status:
			fmt.Fprintf(notes, "%s: Status is %s, changed since Metronome set %s; left alone\n", issue.ref(), issue.Status, entry.Status)
		case issue.Status == target:
			entry.Status = target
		default:
			if err := g.setStatus(ctx, board, issue, target); err != nil {
				return err
			}
			entry.Status, issue.Status = target, target
			fmt.Fprintf(out, "set %s %s\n", issue.ref(), target)
		}
		if err := save(); err != nil {
			return err
		}
	}
	if record.Transition == "done" && !strings.EqualFold(issue.State, "closed") {
		if _, err := g.call(ctx, "api", "-X", "PATCH", endpoint, "-f", "state=closed", "-f", "state_reason=completed"); err != nil {
			return err
		}
		issue.State = "closed"
		fmt.Fprintf(out, "closed %s\n", issue.ref())
	}
	if record.Transition == "done" || record.Transition == "dropped" {
		if includesFold(issue.Labels, label) {
			if _, err := g.call(ctx, "api", "-X", "DELETE", endpoint+"/labels/"+url.PathEscape(label)); err != nil {
				return err
			}
			issue.Labels = slices.DeleteFunc(issue.Labels, func(value string) bool { return strings.EqualFold(value, label) })
		}
		if entry.Assigned != "" && includesFold(issue.Assignees, entry.Assigned) {
			if _, err := g.call(ctx, "api", "-X", "DELETE", endpoint+"/assignees", "-f", "assignees[]="+entry.Assigned); err != nil {
				return err
			}
			issue.Assignees = slices.DeleteFunc(issue.Assignees, func(value string) bool { return strings.EqualFold(value, entry.Assigned) })
		}
	}
	return nil
}

func (g GH) hasComment(ctx context.Context, endpoint, marker string) (bool, error) {
	data, err := g.call(ctx, "api", "--paginate", endpoint+"/comments")
	if err != nil {
		return false, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	for page := 0; ; page++ {
		var comments []struct{ Body string }
		if err := dec.Decode(&comments); err == io.EOF && page > 0 {
			return false, nil
		} else if err != nil {
			return false, err
		}
		for _, comment := range comments {
			if strings.Contains(comment.Body, marker) {
				return true, nil
			}
		}
	}
}
