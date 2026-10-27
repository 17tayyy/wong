// Package linear reads issue states from Linear's GraphQL API.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

const Endpoint = "https://api.linear.app/graphql"

const issuesQuery = `query($team: String!, $numbers: [Float!]) {
  issues(first: 250, includeArchived: true, filter: {team: {key: {eq: $team}}, number: {in: $numbers}}) {
    nodes { identifier completedAt canceledAt state { name type } }
  }
}`

type Issue struct {
	Identifier string
	State      string
	Type       string
	ClosedAt   time.Time
}

func (i Issue) Closed() bool {
	return i.Type == "completed" || i.Type == "canceled"
}

type Client struct {
	Key  string
	URL  string
	HTTP *http.Client
}

func New(key string) *Client {
	return &Client{Key: key, URL: Endpoint, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) TeamKeys(ctx context.Context) ([]string, error) {
	var out struct {
		Teams struct{ Nodes []struct{ Key string } }
	}
	if err := c.query(ctx, `query { teams(first: 250) { nodes { key } } }`, nil, &out); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(out.Teams.Nodes))
	for _, t := range out.Teams.Nodes {
		keys = append(keys, t.Key)
	}
	return keys, nil
}

// Issues returns the issues that exist among ids such as "ENG-76", keyed by identifier.
func (c *Client) Issues(ctx context.Context, ids []string) (map[string]Issue, error) {
	byTeam := map[string][]int{}
	for _, id := range ids {
		team, number, _ := strings.Cut(id, "-")
		n, err := strconv.Atoi(number)
		if err != nil || team == "" {
			return nil, fmt.Errorf("invalid issue id %q", id)
		}
		byTeam[team] = append(byTeam[team], n)
	}
	issues := map[string]Issue{}
	for team, numbers := range byTeam {
		for chunk := range slices.Chunk(numbers, 250) {
			var out struct {
				Issues struct {
					Nodes []struct {
						Identifier  string
						CompletedAt *time.Time
						CanceledAt  *time.Time
						State       struct{ Name, Type string }
					}
				}
			}
			if err := c.query(ctx, issuesQuery, map[string]any{"team": team, "numbers": chunk}, &out); err != nil {
				return nil, err
			}
			for _, n := range out.Issues.Nodes {
				issue := Issue{Identifier: n.Identifier, State: n.State.Name, Type: n.State.Type}
				switch {
				case n.CompletedAt != nil:
					issue.ClosedAt = *n.CompletedAt
				case n.CanceledAt != nil:
					issue.ClosedAt = *n.CanceledAt
				}
				issues[n.Identifier] = issue
			}
		}
	}
	return issues, nil
}

func (c *Client) query(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.Key)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("linear: %w", err)
	}
	defer resp.Body.Close()
	var envelope struct {
		Data   json.RawMessage
		Errors []struct{ Message string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("linear: %s: %w", resp.Status, err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("linear: %s", envelope.Errors[0].Message)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear: %s", resp.Status)
	}
	return json.Unmarshal(envelope.Data, out)
}
