package linear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

type request struct {
	Query     string
	Variables map[string]any
}

func server(t *testing.T, handle func(r request) string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "lin_api_test" {
			t.Errorf("Authorization = %q", got)
		}
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(handle(req)))
	}))
	t.Cleanup(srv.Close)
	c := New("lin_api_test")
	c.URL = srv.URL
	return c
}

func TestTeamKeys(t *testing.T) {
	c := server(t, func(request) string {
		return `{"data":{"teams":{"nodes":[{"key":"ENG"},{"key":"OPS"}]}}}`
	})

	keys, err := c.TeamKeys(context.Background())
	if err != nil || !slices.Equal(keys, []string{"ENG", "OPS"}) {
		t.Errorf("TeamKeys() = %v, %v", keys, err)
	}
}

func TestIssues(t *testing.T) {
	c := server(t, func(r request) string {
		if !strings.Contains(r.Query, "includeArchived: true") {
			t.Errorf("query does not include archived issues: %s", r.Query)
		}
		if r.Variables["team"] != "ENG" || len(r.Variables["numbers"].([]any)) != 3 {
			t.Errorf("variables = %v", r.Variables)
		}
		return `{"data":{"issues":{"nodes":[
			{"identifier":"ENG-76","completedAt":"2026-10-02T09:00:00.000Z","canceledAt":null,"state":{"name":"Done","type":"completed"}},
			{"identifier":"ENG-15","completedAt":null,"canceledAt":"2026-09-01T00:00:00.000Z","state":{"name":"Canceled","type":"canceled"}},
			{"identifier":"ENG-101","completedAt":null,"canceledAt":null,"state":{"name":"Todo","type":"unstarted"}}
		]}}}`
	})

	issues, err := c.Issues(context.Background(), []string{"ENG-76", "ENG-15", "ENG-101"})
	if err != nil {
		t.Fatal(err)
	}

	done := issues["ENG-76"]
	if !done.Closed() || done.State != "Done" || !done.ClosedAt.Equal(time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("ENG-76 = %+v", done)
	}
	if canceled := issues["ENG-15"]; !canceled.Closed() || canceled.ClosedAt.IsZero() {
		t.Errorf("ENG-15 = %+v", canceled)
	}
	if open := issues["ENG-101"]; open.Closed() || !open.ClosedAt.IsZero() {
		t.Errorf("ENG-101 = %+v", open)
	}
}

func TestIssuesRejectsInvalidID(t *testing.T) {
	if _, err := New("k").Issues(context.Background(), []string{"ENG76"}); err == nil {
		t.Error("Issues() accepted an invalid id")
	}
}

func TestQueryReportsGraphQLErrors(t *testing.T) {
	c := server(t, func(request) string {
		return `{"errors":[{"message":"Authentication required"}]}`
	})

	if _, err := c.TeamKeys(context.Background()); err == nil || !strings.Contains(err.Error(), "Authentication required") {
		t.Errorf("TeamKeys() error = %v", err)
	}
}
