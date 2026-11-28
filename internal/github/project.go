package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const projectPublishQuery = `query($owner: String!, $number: Int!, $field: String!, $endCursor: String) {
  repositoryOwner(login: $owner) { ... on ProjectV2Owner { projectV2(number: $number) {
    id
    field(name: $field) { __typename ... on ProjectV2SingleSelectField { id options { id name } } }
    items(first: 100, after: $endCursor) { pageInfo { hasNextPage endCursor } nodes {
      id
      content { __typename ... on Issue { number state body repository { nameWithOwner } assignees(first: 100) { nodes { login } } labels(first: 100) { nodes { name } } } }
      fieldValueByName(name: $field) { ... on ProjectV2ItemFieldSingleSelectValue { name optionId } }
    } }
  } } }
}`

type projectIssue struct {
	IsPullRequest                 bool
	ID, Repo, Body, Status, State string
	Number                        int
	Assignees, Labels             []string
}

func (i *projectIssue) ref() string      { return fmt.Sprintf("%s#%d", i.Repo, i.Number) }
func (i *projectIssue) endpoint() string { return fmt.Sprintf("repos/%s/issues/%d", i.Repo, i.Number) }

type projectBoard struct {
	ID, Field string
	Options   map[string]string
	Issues    []*projectIssue
}

func (g GH) call(ctx context.Context, args ...string) ([]byte, error) {
	if g.Run != nil {
		return g.Run(ctx, args...)
	}
	data, err := runGH(ctx, g.Environ, args...)
	if err != nil && g.Redact != nil {
		return nil, fmt.Errorf("%s", g.Redact(err.Error()))
	}
	if g.Holds != nil && g.Holds(data) {
		return nil, fmt.Errorf("gh output contains a GitHub App credential")
	}
	return data, err
}

func (g GH) scanProject(ctx context.Context, project Project, field string) (projectBoard, error) {
	data, err := g.call(ctx, "api", "graphql", "--paginate", "-f", "query="+projectPublishQuery, "-f", "owner="+project.Owner, "-F", "number="+strconv.Itoa(project.Number), "-f", "field="+field)
	if err != nil {
		return projectBoard{}, err
	}
	return parsePublishProject(data)
}

func parsePublishProject(data []byte) (projectBoard, error) {
	board := projectBoard{Options: map[string]string{}}
	decoder := json.NewDecoder(bytes.NewReader(data))
	more := true
	for more {
		var response struct {
			Errors []struct{ Message string }
			Data   struct {
				RepositoryOwner struct {
					ProjectV2 *struct {
						ID    string
						Field *struct {
							Typename string `json:"__typename"`
							ID       string
							Options  []struct{ ID, Name string }
						}
						Items struct {
							PageInfo struct{ HasNextPage *bool }
							Nodes    []struct {
								ID      string
								Content *struct {
									Typename    string `json:"__typename"`
									Number      int
									State, Body string
									Repository  struct{ NameWithOwner string }
									Assignees   struct{ Nodes []struct{ Login string } }
									Labels      struct{ Nodes []struct{ Name string } }
								}
								FieldValueByName *struct{ Name, OptionID string }
							}
						}
					}
				}
			}
		}
		if err := decoder.Decode(&response); err != nil {
			return board, fmt.Errorf("project response: %w", err)
		}
		if len(response.Errors) > 0 {
			return board, fmt.Errorf("project: %s", response.Errors[0].Message)
		}
		p := response.Data.RepositoryOwner.ProjectV2
		if p == nil || p.ID == "" || p.Field == nil || p.Field.ID == "" || p.Field.Typename != "ProjectV2SingleSelectField" {
			return board, fmt.Errorf("project or single-select field not found")
		}
		if p.Items.PageInfo.HasNextPage == nil || p.Items.Nodes == nil {
			return board, fmt.Errorf("missing project items or page info")
		}
		board.ID, board.Field = p.ID, p.Field.ID
		for _, option := range p.Field.Options {
			board.Options[option.Name] = option.ID
		}
		for _, node := range p.Items.Nodes {
			c := node.Content
			if c == nil || c.Typename != "Issue" {
				continue
			}
			issue := &projectIssue{ID: node.ID, Repo: c.Repository.NameWithOwner, Number: c.Number, State: c.State, Body: c.Body}
			if node.FieldValueByName != nil {
				issue.Status = node.FieldValueByName.Name
			}
			for _, a := range c.Assignees.Nodes {
				issue.Assignees = append(issue.Assignees, a.Login)
			}
			for _, label := range c.Labels.Nodes {
				issue.Labels = append(issue.Labels, label.Name)
			}
			board.Issues = append(board.Issues, issue)
		}
		more = *p.Items.PageInfo.HasNextPage
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return board, fmt.Errorf("unexpected extra project response")
	}
	return board, nil
}

const setStatusMutation = `mutation($project: ID!, $item: ID!, $field: ID!, $option: String!) { updateProjectV2ItemFieldValue(input: {projectId: $project, itemId: $item, fieldId: $field, value: {singleSelectOptionId: $option}}) { projectV2Item { id } } }`

func (g GH) setStatus(ctx context.Context, board projectBoard, issue *projectIssue, target string) error {
	data, err := g.call(ctx, "api", "graphql", "-f", "query="+setStatusMutation, "-f", "project="+board.ID, "-f", "item="+issue.ID, "-f", "field="+board.Field, "-f", "option="+board.Options[target])
	if err != nil {
		return err
	}
	var response struct {
		Errors []struct{ Message string }
		Data   struct {
			UpdateProjectV2ItemFieldValue *struct{ ProjectV2Item struct{ ID string } }
		}
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return err
	}
	if len(response.Errors) > 0 {
		return fmt.Errorf("set status: %s", response.Errors[0].Message)
	}
	if response.Data.UpdateProjectV2ItemFieldValue == nil || response.Data.UpdateProjectV2ItemFieldValue.ProjectV2Item.ID != issue.ID {
		return fmt.Errorf("missing updated project item")
	}
	return nil
}

func (g GH) getIssue(ctx context.Context, repo string, number int) (*projectIssue, error) {
	issue := &projectIssue{Repo: repo, Number: number}
	data, err := g.call(ctx, "api", issue.endpoint())
	if err != nil {
		return nil, err
	}
	var response struct {
		Number      int
		State, Body string
		Assignees   []struct{ Login string }
		Labels      []struct{ Name string }
		PullRequest json.RawMessage `json:"pull_request"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	if response.Number != number || !strings.EqualFold(response.State, "open") && !strings.EqualFold(response.State, "closed") {
		return nil, fmt.Errorf("invalid issue response for %s", issue.ref())
	}
	issue.IsPullRequest = response.PullRequest != nil
	issue.State, issue.Body = response.State, response.Body
	for _, a := range response.Assignees {
		issue.Assignees = append(issue.Assignees, a.Login)
	}
	for _, label := range response.Labels {
		issue.Labels = append(issue.Labels, label.Name)
	}
	return issue, nil
}
