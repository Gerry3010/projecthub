// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, either version 3 of the
// License, or (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

// Command phmcp is the ProjectHub MCP bridge: a stdio MCP server that Claude Code
// launches (via .mcp.json) and that forwards tool calls to the running ProjectHub
// sidecar. It finds the sidecar's per-launch loopback endpoint + bearer token from
// the discovery file phd writes (internal/discovery), exactly like cmd/tabhost — so a
// Claude-Code process started inside an embedded terminal can drive its own project's
// workspace and vault without ever knowing the random port/token up front.
//
// Protocol: JSON-RPC 2.0 over newline-delimited stdio (the MCP stdio transport). The
// tool catalog is served from internal/mcp; tools/call POSTs to the sidecar's
// /native/mcp/call, which runs local tools directly and forwards renderer tools to
// the WASM UI over the control channel.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/Gerry3010/projecthub/internal/buildinfo"
	"github.com/Gerry3010/projecthub/internal/discovery"
	"github.com/Gerry3010/projecthub/internal/mcp"
)

const protocolVersion = "2024-11-05"

// rpcRequest / rpcResponse are minimal JSON-RPC 2.0 envelopes.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // absent ⇒ notification
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func main() {
	out := bufio.NewWriter(os.Stdout)
	defer func() { _ = out.Flush() }()
	enc := json.NewEncoder(out)

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // allow large tool payloads
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue // malformed line; skip
		}
		resp, isNotification := handle(req)
		if isNotification {
			continue // notifications get no reply
		}
		_ = enc.Encode(resp)
		_ = out.Flush()
	}
}

func handle(req rpcRequest) (rpcResponse, bool) {
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "projecthub", "version": buildinfo.Get().Version},
		}
	case "notifications/initialized", "notifications/cancelled":
		return resp, true // notification, no reply
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": mcp.Tools()}
	case "tools/call":
		result, err := callTool(req.Params)
		if err != nil {
			// MCP convention: tool failures are returned as isError content, not a
			// protocol error, so the model can see and react to them.
			resp.Result = map[string]any{
				"content": []map[string]string{{"type": "text", "text": err.Error()}},
				"isError": true,
			}
			return resp, false
		}
		resp.Result = map[string]any{
			"content": []map[string]string{{"type": "text", "text": result}},
		}
	default:
		if len(req.ID) == 0 {
			return resp, true // unknown notification
		}
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp, false
}

// callTool forwards a tools/call to the sidecar and returns the tool's raw JSON
// result as text.
func callTool(params json.RawMessage) (string, error) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", fmt.Errorf("bad params: %w", err)
	}
	if !mcp.Known(p.Name) {
		return "", fmt.Errorf("unknown tool: %s", p.Name)
	}
	ep, err := discovery.Read()
	if err != nil {
		return "", fmt.Errorf("ProjectHub sidecar not found (is the app running?): %w", err)
	}
	body, _ := json.Marshal(map[string]any{"tool": p.Name, "args": p.Arguments})
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.Base+"/native/mcp/call", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	req.Header.Set("Content-Type", "application/json")
	// Stamp ourselves so app_info can report the bridge's own build: this binary is
	// launched from wherever Claude Code was configured, which is not necessarily the
	// bundle the running app came from.
	if stamp, err := json.Marshal(buildinfo.Get()); err == nil {
		req.Header.Set("X-ProjectHub-Client", string(stamp))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("%s: %s", resp.Status, string(bytes.TrimSpace(data)))
	}
	return string(data), nil
}
