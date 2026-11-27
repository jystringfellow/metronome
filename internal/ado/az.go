package ado

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

type AZ struct {
	Run func(ctx context.Context, args ...string) ([]byte, error)
}

type Target struct {
	Org, Project, WIQL, QueryID string
}

type Source interface {
	Query(ctx context.Context, target Target) ([]int, error)
	WorkItems(ctx context.Context, org, project string, ids []int) ([]WorkItem, error)
}

type WorkItem struct {
	ID                                                        int
	Title, State, AssignedTo, Description, AcceptanceCriteria string
	Rank                                                      *float64
	Tags                                                      []string
	Predecessors                                              []int
}

func runAZ(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "az", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("az devops invoke: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (az AZ) invoke(ctx context.Context, org, project, resource, id string, body any) ([]byte, error) {
	args := []string{"devops", "invoke", "--org", org, "--area", "wit", "--resource", resource, "--route-parameters", "project=" + project}
	if id != "" {
		args = append(args, "id="+id)
	}
	if body == nil {
		args = append(args, "--http-method", "GET")
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		dir, err := os.MkdirTemp("", "metronome-ado-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		file := filepath.Join(dir, "request.json")
		if err := os.WriteFile(file, data, 0600); err != nil {
			return nil, err
		}
		args = append(args, "--http-method", "POST", "--in-file", file)
	}
	args = append(args, "--api-version", "7.1", "--detect", "false", "--output", "json")
	run := az.Run
	if run == nil {
		run = runAZ
	}
	return run(ctx, args...)
}

func (az AZ) Query(ctx context.Context, target Target) ([]int, error) {
	var body any
	if target.QueryID == "" {
		body = map[string]string{"query": target.WIQL}
	}
	data, err := az.invoke(ctx, target.Org, target.Project, "wiql", target.QueryID, body)
	if err != nil {
		return nil, err
	}
	return ParseQuery(data)
}

func ParseQuery(data []byte) ([]int, error) {
	var result struct {
		QueryResultType string `json:"queryResultType"`
		WorkItems       []struct {
			ID int `json:"id"`
		} `json:"workItems"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("WIQL result: %w", err)
	}
	if result.QueryResultType != "workItem" {
		return nil, fmt.Errorf("the query does not return flat work items; use a flat query")
	}
	if result.WorkItems == nil {
		return nil, fmt.Errorf("WIQL result is missing workItems")
	}
	ids := make([]int, 0, len(result.WorkItems))
	seen := map[int]bool{}
	for _, item := range result.WorkItems {
		if item.ID <= 0 || seen[item.ID] {
			return nil, fmt.Errorf("invalid or duplicate work item ID %d", item.ID)
		}
		seen[item.ID] = true
		ids = append(ids, item.ID)
	}
	return ids, nil
}

func ParseWorkItems(data []byte) ([]WorkItem, error) {
	var result struct {
		Count *int `json:"count"`
		Value []struct {
			ID     int `json:"id"`
			Fields *struct {
				Title              string                                    `json:"System.Title"`
				State              string                                    `json:"System.State"`
				AssignedTo         *struct{ UniqueName, DisplayName string } `json:"System.AssignedTo"`
				Description        string                                    `json:"System.Description"`
				AcceptanceCriteria string                                    `json:"Microsoft.VSTS.Common.AcceptanceCriteria"`
				Priority           *float64                                  `json:"Microsoft.VSTS.Common.BacklogPriority"`
				StackRank          *float64                                  `json:"Microsoft.VSTS.Common.StackRank"`
				Tags               string                                    `json:"System.Tags"`
			} `json:"fields"`
			Relations []struct{ Rel, URL string } `json:"relations"`
		} `json:"value"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("work item batch: %w", err)
	}
	if result.Count == nil || result.Value == nil || *result.Count != len(result.Value) {
		return nil, fmt.Errorf("work item batch count or value missing or inconsistent")
	}
	items := make([]WorkItem, 0, len(result.Value))
	for _, raw := range result.Value {
		fields := raw.Fields
		if raw.ID <= 0 || fields == nil || strings.TrimSpace(fields.State) == "" {
			return nil, fmt.Errorf("AB#%d has missing fields or state, or invalid ID", raw.ID)
		}
		item := WorkItem{ID: raw.ID, Title: fields.Title, State: strings.TrimSpace(fields.State), Description: fields.Description, AcceptanceCriteria: fields.AcceptanceCriteria, Rank: fields.Priority}
		if item.Rank == nil {
			item.Rank = fields.StackRank
		}
		if fields.AssignedTo != nil {
			item.AssignedTo = strings.TrimSpace(fields.AssignedTo.UniqueName)
			if item.AssignedTo == "" {
				item.AssignedTo = strings.TrimSpace(fields.AssignedTo.DisplayName)
			}
			if item.AssignedTo == "" {
				return nil, fmt.Errorf("AB#%d has an assigned identity without a name", raw.ID)
			}
		}
		for _, tag := range strings.Split(fields.Tags, ";") {
			if tag = strings.TrimSpace(tag); tag != "" {
				item.Tags = append(item.Tags, tag)
			}
		}
		for _, rel := range raw.Relations {
			if rel.Rel != "System.LinkTypes.Dependency-Reverse" {
				continue
			}
			parsed, err := url.Parse(rel.URL)
			if err != nil {
				return nil, fmt.Errorf("AB#%d: invalid predecessor URL %q", raw.ID, rel.URL)
			}
			id, err := strconv.Atoi(path.Base(parsed.Path))
			if err != nil || id <= 0 {
				return nil, fmt.Errorf("AB#%d: invalid predecessor URL %q", raw.ID, rel.URL)
			}
			item.Predecessors = append(item.Predecessors, id)
		}
		items = append(items, item)
	}
	return items, nil
}

func (az AZ) WorkItems(ctx context.Context, org, project string, ids []int) ([]WorkItem, error) {
	items := make([]WorkItem, 0, len(ids))
	for start := 0; start < len(ids); start += 200 {
		batch := ids[start:min(start+200, len(ids))]
		body := map[string]any{"ids": batch, "$expand": "relations", "errorPolicy": "fail"}
		data, err := az.invoke(ctx, org, project, "workitemsbatch", "", body)
		if err != nil {
			return nil, err
		}
		found, err := ParseWorkItems(data)
		if err != nil {
			return nil, err
		}
		byID := map[int]WorkItem{}
		for _, item := range found {
			if _, exists := byID[item.ID]; exists {
				return nil, fmt.Errorf("duplicate work item AB#%d", item.ID)
			}
			byID[item.ID] = item
		}
		for _, id := range batch {
			item, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("work item batch missing AB#%d", id)
			}
			items = append(items, item)
			delete(byID, id)
		}
		if len(byID) != 0 {
			return nil, fmt.Errorf("work item batch returned unexpected IDs")
		}
	}
	return items, nil
}
