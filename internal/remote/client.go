package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	URL   string
	Token string
	HTTP  *http.Client
}

func NewClient(baseURL, token string, timeout time.Duration) *Client {
	return &Client{URL: strings.TrimSuffix(baseURL, "/"), Token: token, HTTP: &http.Client{Timeout: timeout}}
}

func (c *Client) Changes(ctx context.Context, since int64) ([]Change, int64, error) {
	var out changesResponse
	err := c.do(ctx, http.MethodGet, "/api/changes?since="+url.QueryEscape(strconv.FormatInt(since, 10)), nil, &out)
	return out.Changes, out.Cursor, err
}

func (c *Client) Push(ctx context.Context, pushes []Upload) ([]string, []Change, error) {
	var out pushResponse
	err := c.do(ctx, http.MethodPost, "/api/push", pushRequest{Changes: pushes}, &out)
	return out.Accepted, out.Conflicts, err
}

// Tasks returns the open tasks of a project, "me" for the global notes.
func (c *Client) Tasks(ctx context.Context, project string) ([]Task, error) {
	var out tasksResponse
	err := c.do(ctx, http.MethodGet, "/api/tasks?project="+url.QueryEscape(project), nil, &out)
	return out.Tasks, err
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("wong server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("wong server: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
