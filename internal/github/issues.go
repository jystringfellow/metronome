package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jystringfellow/metronome/internal/flow"
)

type Issue struct {
	Number            int
	Title, Body       string
	Assignees, Labels []string
	CreatedAt         time.Time
}

type Project struct {
	Owner  string
	Number int
}

func ParseProject(value string) (Project, error) {
	owner, number, ok := strings.Cut(value, "/")
	n, err := strconv.Atoi(number)
	if !ok || owner == "" || strings.ContainsFunc(owner, unicode.IsSpace) || err != nil || n <= 0 {
		return Project{}, fmt.Errorf("project must be OWNER/NUMBER with a positive number")
	}
	return Project{Owner: owner, Number: n}, nil
}

type IssueSource interface {
	LinkedPRs(ctx context.Context, repo, label string) (map[int][]string, error)
	OpenIssues(ctx context.Context, repo, label string) ([]Issue, error)
	ProjectRanks(ctx context.Context, project Project, field, repo string) (map[int]float64, error)
}

const linkedPRsQuery = `query($owner: String!, $repo: String!, $label: String!, $endCursor: String) {
 repository(owner: $owner, name: $repo) {
  issues(first: 100, after: $endCursor, states: OPEN, labels: [$label]) {
   pageInfo { hasNextPage endCursor }
   nodes { number closedByPullRequestsReferences(first: 10, includeClosedPrs: false) { totalCount nodes { number repository { nameWithOwner } } } }
  }
 }
}`

func (g GH) LinkedPRs(ctx context.Context, repo, label string) (map[int][]string, error) {
	owner, name, _ := strings.Cut(repo, "/")
	data, err := g.call(ctx, "api", "graphql", "--paginate", "-f", "query="+linkedPRsQuery, "-f", "owner="+owner, "-f", "repo="+name, "-f", "label="+label)
	if err != nil {
		return nil, err
	}
	return ParseLinkedPRs(data)
}

func ParseLinkedPRs(data []byte) (map[int][]string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	linked := map[int][]string{}
	more := true
	for more {
		var response struct {
			Errors []struct{ Message string }
			Data   struct {
				Repository *struct {
					Issues *struct {
						PageInfo struct {
							HasNextPage *bool
							EndCursor   string
						}
						Nodes []struct {
							Number                         int
							ClosedByPullRequestsReferences *struct {
								TotalCount *int
								Nodes      []struct {
									Number     int
									Repository struct{ NameWithOwner string }
								}
							}
						}
					}
				}
			}
		}
		if err := dec.Decode(&response); err != nil {
			return nil, fmt.Errorf("linked PRs: %w", err)
		}
		if len(response.Errors) > 0 {
			return nil, fmt.Errorf("linked PRs: %s", response.Errors[0].Message)
		}
		if response.Data.Repository == nil || response.Data.Repository.Issues == nil {
			return nil, fmt.Errorf("linked PRs: missing issues")
		}
		page := response.Data.Repository.Issues
		if page.PageInfo.HasNextPage == nil || page.Nodes == nil {
			return nil, fmt.Errorf("linked PRs: missing page information")
		}
		more = *page.PageInfo.HasNextPage
		if more && page.PageInfo.EndCursor == "" {
			return nil, fmt.Errorf("linked PRs: missing next page cursor")
		}
		for _, issue := range page.Nodes {
			refs := issue.ClosedByPullRequestsReferences
			if refs == nil || refs.TotalCount == nil || *refs.TotalCount != len(refs.Nodes) || *refs.TotalCount > 10 {
				return nil, fmt.Errorf("issue %d linked PRs missing or truncated", issue.Number)
			}
			for _, pr := range refs.Nodes {
				linked[issue.Number] = append(linked[issue.Number], FormatPRRef(pr.Repository.NameWithOwner, pr.Number))
			}
			refsNormalised, err := flow.NormalisePRs(linked[issue.Number])
			if err != nil {
				return nil, err
			}
			linked[issue.Number] = refsNormalised
		}
	}
	if dec.Decode(new(json.RawMessage)) != io.EOF {
		return nil, fmt.Errorf("linked PRs: unexpected data after final page")
	}
	return linked, nil
}

func (g GH) OpenIssues(ctx context.Context, repo, label string) ([]Issue, error) {
	query := url.Values{"state": {"open"}, "labels": {label}, "per_page": {"100"}, "sort": {"created"}, "direction": {"asc"}}
	data, err := g.call(ctx, "api", "--paginate", "repos/"+repo+"/issues?"+query.Encode())
	if err != nil {
		return nil, err
	}
	return ParseIssues(data)
}

const projectRanksQuery = `query($owner: String!, $number: Int!, $field: String!, $endCursor: String) {
  repositoryOwner(login: $owner) {
    ... on ProjectV2Owner {
      projectV2(number: $number) {
        field(name: $field) { __typename ... on ProjectV2Field { dataType } }
        items(first: 100, after: $endCursor) {
          pageInfo { hasNextPage endCursor }
          nodes {
            content { __typename ... on Issue { number repository { nameWithOwner } } }
            fieldValueByName(name: $field) { __typename ... on ProjectV2ItemFieldNumberValue { number } }
          }
        }
      }
    }
  }
}`

func (g GH) ProjectRanks(ctx context.Context, project Project, field, repo string) (map[int]float64, error) {
	data, err := g.call(ctx, "api", "graphql", "--paginate", "-f", "query="+projectRanksQuery, "-f", "owner="+project.Owner, "-F", "number="+strconv.Itoa(project.Number), "-f", "field="+field)
	if err != nil {
		return nil, err
	}
	ranks, err := ParseProjectRanks(repo, data)
	if err != nil {
		return nil, fmt.Errorf("project %s/%d field %s: %w", project.Owner, project.Number, field, err)
	}
	return ranks, nil
}

func ParseIssues(data []byte) ([]Issue, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	issues := []Issue{}
	for page := 0; ; page++ {
		var entries []struct {
			Number      int
			Title, Body string
			CreatedAt   time.Time `json:"created_at"`
			Assignees   []struct{ Login string }
			Labels      []struct{ Name string }
			PullRequest *json.RawMessage `json:"pull_request"`
		}
		err := dec.Decode(&entries)
		if err == io.EOF && page > 0 {
			return issues, nil
		}
		if err != nil {
			return nil, fmt.Errorf("gh api issues output: %w", err)
		}
		if entries == nil {
			return nil, fmt.Errorf("gh api issues output: expected an array")
		}
		for _, entry := range entries {
			if entry.PullRequest != nil {
				continue
			}
			issue := Issue{Number: entry.Number, Title: entry.Title, Body: entry.Body, CreatedAt: entry.CreatedAt}
			for _, assignee := range entry.Assignees {
				issue.Assignees = append(issue.Assignees, assignee.Login)
			}
			for _, label := range entry.Labels {
				issue.Labels = append(issue.Labels, label.Name)
			}
			issues = append(issues, issue)
		}
	}
}

type projectResponse struct {
	Errors []struct{ Message string }
	Data   struct {
		RepositoryOwner *struct {
			ProjectV2 *struct {
				Field *struct {
					Typename string `json:"__typename"`
					DataType string
				}
				Items struct {
					PageInfo struct{ HasNextPage *bool }
					Nodes    []struct {
						Content *struct {
							Typename   string `json:"__typename"`
							Number     int
							Repository struct{ NameWithOwner string }
						}
						FieldValueByName *struct {
							Typename string `json:"__typename"`
							Number   *float64
						}
					}
				}
			}
		}
	}
}

func ParseProjectRanks(repo string, data []byte) (map[int]float64, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	ranks := map[int]float64{}
	more := false
	for page := 0; ; page++ {
		var response projectResponse
		err := dec.Decode(&response)
		if err == io.EOF && page > 0 && !more {
			return ranks, nil
		}
		if err != nil {
			return nil, fmt.Errorf("gh api graphql output: %w", err)
		}
		if len(response.Errors) > 0 {
			return nil, fmt.Errorf("gh api graphql: %s", response.Errors[0].Message)
		}
		owner := response.Data.RepositoryOwner
		if owner == nil || owner.ProjectV2 == nil {
			return nil, fmt.Errorf("project not found or not readable by gh")
		}
		project := owner.ProjectV2
		if project.Field == nil {
			return nil, fmt.Errorf("project has no field with that name")
		}
		if project.Field.Typename != "ProjectV2Field" || project.Field.DataType != "NUMBER" {
			return nil, fmt.Errorf("field is not a number field")
		}
		if project.Items.PageInfo.HasNextPage == nil || project.Items.Nodes == nil {
			return nil, fmt.Errorf("gh api graphql output: missing project items or page information")
		}
		more = *project.Items.PageInfo.HasNextPage
		for _, node := range project.Items.Nodes {
			content, value := node.Content, node.FieldValueByName
			if content == nil || content.Typename != "Issue" || !strings.EqualFold(content.Repository.NameWithOwner, repo) || value == nil {
				continue
			}
			if value.Typename != "ProjectV2ItemFieldNumberValue" || value.Number == nil {
				return nil, fmt.Errorf("issue %s#%d has an invalid rank value", repo, content.Number)
			}
			ranks[content.Number] = *value.Number
		}
	}
}
