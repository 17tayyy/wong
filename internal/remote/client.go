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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/seal"
)

// Client talks to a wong server. With this machine's Device, it opens the key of a sealed brain the first time it
// syncs, and from then on its Key encrypts what it pushes and decrypts what it pulls.
type Client struct {
	URL    string
	Token  string
	HTTP   *http.Client
	Device *seal.Device
	Key    *seal.Key
	State  string
	clear  bool
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

// ReportFindings sends what this machine's check found in a project, replacing its previous report. A sealed brain
// gets none, since findings name notes and quote them.
func (c *Client) ReportFindings(ctx context.Context, project string, findings []check.Finding) error {
	if err := c.Unlock(ctx); err != nil || c.Key != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/api/findings", findingsRequest{Project: project, Findings: findings}, &struct{}{})
}

// Keys returns the brain's keyring as this machine sees it; a server too old to keep one has a brain in the clear.
func (c *Client) Keys(ctx context.Context) (Keys, error) {
	var out Keys
	resp, err := c.send(ctx, http.MethodGet, "/api/keys", nil)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		err = json.NewDecoder(resp.Body).Decode(&out)
	case http.StatusNotFound, http.StatusMethodNotAllowed:
	default:
		err = failed(resp)
	}
	return out, err
}

// StartSeal makes the server's brain end-to-end encrypted, with key wrapped for this machine and the recovery code; the
// files it holds in the clear, which this machine pulled up to cursor, go from the server.
func (c *Client) StartSeal(ctx context.Context, device, recovery *seal.Device, key *seal.Key, cursor int64) error {
	wrapped, err := seal.Wrap(key, device.Public())
	if err != nil {
		return err
	}
	recoveryWrapped, err := seal.Wrap(key, recovery.Public())
	if err != nil {
		return err
	}
	start := sealStart{Public: device.Public(), Wrapped: wrapped, RecoveryPublic: recovery.Public(), RecoveryWrapped: recoveryWrapped, Cursor: cursor}
	return c.do(ctx, http.MethodPost, "/api/keys/start", start, &struct{}{})
}

// RequestKey asks for the brain's key for this machine, to be approved from one that holds it.
func (c *Client) RequestKey(ctx context.Context, device *seal.Device) error {
	return c.do(ctx, http.MethodPost, "/api/keys/request", keyApproval{Public: device.Public()}, &struct{}{})
}

// ApproveKey gives a waiting device the brain's key, wrapped for the public key the person checked.
func (c *Client) ApproveKey(ctx context.Context, req KeyRequest, key *seal.Key) error {
	wrapped, err := seal.Wrap(key, req.Public)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/api/keys/approve", keyApproval{Device: req.Device, Public: req.Public, Wrapped: wrapped}, &struct{}{})
}

// KeepOwnKey stores the brain's key wrapped for this machine, after the recovery code opened it.
func (c *Client) KeepOwnKey(ctx context.Context, device *seal.Device, key *seal.Key) error {
	wrapped, err := seal.Wrap(key, device.Public())
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/api/keys/own", keyApproval{Public: device.Public(), Wrapped: wrapped}, &struct{}{})
}

// LetCloudAIs lets the AIs that run in the cloud read the sealed brain, giving the server key wrapped for its own key.
func (c *Client) LetCloudAIs(ctx context.Context, key *seal.Key) error {
	keys, err := c.Keys(ctx)
	if err != nil {
		return err
	}
	if keys.CloudPublic == nil {
		return ErrCloudAIsOff
	}
	wrapped, err := seal.Wrap(key, keys.CloudPublic)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/api/keys/cloud-ais", keyApproval{Wrapped: wrapped}, &struct{}{})
}

// StopCloudAIs closes the sealed brain to the AIs that run in the cloud: the server forgets the key it was given.
func (c *Client) StopCloudAIs(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, "/api/keys/cloud-ais", struct{}{}, &struct{}{})
}

// ErrLocked marks a sealed brain whose key this machine does not hold yet.
var ErrLocked = errors.New("this brain is end-to-end encrypted and this machine has no key yet; it asked for one: run wong keys to see its code and approve it from a machine that has the key with wong keys approve, or use the recovery code here with wong keys recover")

// Unlock learns whether the brain is sealed and, when it is, opens its key with this machine's Device; a client
// that has not unlocked a sealed brain never syncs it, and asks for its key, for a machine that holds it to approve.
func (c *Client) Unlock(ctx context.Context) error {
	if c.Key != nil || c.clear {
		return nil
	}
	keys, err := c.Keys(ctx)
	if err != nil {
		return err
	}
	if !keys.Sealed {
		c.clear = true
		return nil
	}
	if keys.Wrapped == nil || c.Device == nil {
		if c.Device != nil && !slices.ContainsFunc(keys.Pending, func(p KeyRequest) bool { return bytes.Equal(p.Public, c.Device.Public()) }) {
			if err := c.RequestKey(ctx, c.Device); err != nil {
				return err
			}
		}
		return ErrLocked
	}
	key, err := c.Device.Unwrap(keys.Wrapped)
	if err != nil {
		return fmt.Errorf("the brain's key does not open with this machine's key: %w", err)
	}
	c.Key = key
	return nil
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
	req.Header.Set(SealHeader, "1")
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
