package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/17tayyy/wong/internal/check"
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

// ReportFindings sends what this machine's check found in a project, replacing its previous report.
func (c *Client) ReportFindings(ctx context.Context, project string, findings []check.Finding) error {
	return c.do(ctx, http.MethodPost, "/api/findings", findingsRequest{Project: project, Findings: findings}, &struct{}{})
}

// ErrNoDevices marks a server too old to let machines in by approving a code.
var ErrNoDevices = errors.New("the server takes tokens only")

// AskDevice asks the server to let this machine in once someone signed in approves the code it returns.
func (c *Client) AskDevice(ctx context.Context, name string) (DeviceStart, error) {
	var out DeviceStart
	resp, err := c.send(ctx, http.MethodPost, "/api/device", deviceAsk{Name: name})
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		err = json.NewDecoder(resp.Body).Decode(&out)
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		err = ErrNoDevices
	default:
		err = failed(resp)
	}
	return out, err
}

// PollDevice asks whether the machine was approved, and returns its token once it was.
func (c *Client) PollDevice(ctx context.Context, secret string) (DeviceStatus, DeviceToken, error) {
	var out DeviceToken
	resp, err := c.send(ctx, http.MethodPost, "/api/device/token", devicePoll{Secret: secret})
	if err != nil {
		return DeviceGone, out, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusAccepted:
		return DeviceWaiting, out, nil
	case http.StatusForbidden:
		return DeviceDenied, out, nil
	case http.StatusGone:
		return DeviceGone, out, nil
	case http.StatusOK:
		err := json.NewDecoder(resp.Body).Decode(&out)
		return DeviceApproved, out, err
	}
	return DeviceGone, out, failed(resp)
}

// SignIn makes a link that opens the dashboard signed in to this machine's brain, once.
func (c *Client) SignIn(ctx context.Context) (SignInLink, error) {
	var out SignInLink
	err := c.do(ctx, http.MethodPost, "/api/signin", nil, &out)
	return out, err
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	resp, err := c.send(ctx, method, path, in)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return failed(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) send(ctx context.Context, method, path string, in any) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, body)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wong server: %w", err)
	}
	return resp, nil
}

func failed(resp *http.Response) error {
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return fmt.Errorf("wong server: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
}
