package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Bridge relays MCP messages from a stdio client to a wong server's endpoint and writes the answers back, one per line.
func Bridge(ctx context.Context, in io.Reader, out io.Writer, endpoint, token string, client *http.Client) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64<<10), maxMessage)
	var legacy string
	write := func(data []byte) error {
		var line bytes.Buffer
		if err := json.Compact(&line, data); err != nil {
			return err
		}
		line.WriteByte('\n')
		_, err := out.Write(line.Bytes())
		return err
	}
	fail := func(id json.RawMessage, code int, msg string) error {
		data, _ := json.Marshal(response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
		return write(data)
	}
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var msg message
		if err := json.Unmarshal(line, &msg); err != nil || msg.Method == "" {
			if err := fail(nil, codeParse, "not a JSON-RPC request or notification"); err != nil {
				return err
			}
			continue
		}
		var p params
		_ = json.Unmarshal(msg.Params, &p)
		version := legacy
		if raw, ok := p.Meta[metaVersion]; ok {
			_ = json.Unmarshal(raw, &version)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(line))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Mcp-Method", headerSafe(msg.Method))
		if version != "" {
			req.Header.Set("MCP-Protocol-Version", version)
		}
		if msg.Method == "tools/call" {
			req.Header.Set("Mcp-Name", headerSafe(p.Name))
		}
		resp, err := client.Do(req)
		if err != nil {
			if msg.ID != nil {
				if err := fail(msg.ID, codeInternal, "the wong server did not answer: "+err.Error()); err != nil {
					return err
				}
			}
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxMessage))
		resp.Body.Close()
		if err != nil || resp.StatusCode == http.StatusAccepted || msg.ID == nil {
			continue
		}
		var answer struct {
			Result struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"result"`
		}
		if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") || json.Unmarshal(body, &answer) != nil {
			text := fmt.Sprintf("the wong server answered %s: %s", resp.Status, strings.TrimSpace(string(body)))
			if err := fail(msg.ID, codeInternal, text); err != nil {
				return err
			}
			continue
		}
		if msg.Method == "initialize" && answer.Result.ProtocolVersion != "" {
			legacy = answer.Result.ProtocolVersion
		}
		if err := write(body); err != nil {
			return err
		}
	}
	return scanner.Err()
}
