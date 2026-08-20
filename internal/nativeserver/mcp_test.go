// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package nativeserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Gerry3010/projecthub/internal/control"
)

func TestMCPToolsListed(t *testing.T) {
	h := newTestServer()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodGet, "/mcp/tools", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("tools status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "tile_create") || !strings.Contains(rec.Body.String(), "session_list") {
		t.Fatalf("tool catalog missing entries: %s", rec.Body.String())
	}
}

func TestMCPLocalToolFileWrite(t *testing.T) {
	h := newTestServer()
	path := filepath.Join(t.TempDir(), "mcp.txt")
	body, _ := json.Marshal(map[string]any{
		"tool": "file_write",
		"args": map[string]string{"path": path, "content": "via mcp"},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/mcp/call", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("file_write status %d body=%s", rec.Code, rec.Body.String())
	}
	// read it back through the read tool
	body, _ = json.Marshal(map[string]any{"tool": "file_read", "args": map[string]string{"path": path}})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/mcp/call", body))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "via mcp") {
		t.Fatalf("file_read mismatch: %d %s", rec.Code, rec.Body.String())
	}
}

// TestMCPRendererToolForwarded exercises the full control path: an mcp/call for a
// renderer tool blocks in hub.Call while a simulated renderer long-polls /control/next
// and answers via /control/result.
func TestMCPRendererToolForwarded(t *testing.T) {
	h := newTestServer()

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]any{"tool": "project_list", "args": map[string]string{}})
		h.ServeHTTP(rec, authedReq(http.MethodPost, "/mcp/call", body))
		done <- rec
	}()

	// Renderer side: long-poll for the command, then post a result.
	pollRec := httptest.NewRecorder()
	h.ServeHTTP(pollRec, authedReq(http.MethodGet, "/control/next", nil))
	if pollRec.Code != http.StatusOK {
		t.Fatalf("control/next status %d", pollRec.Code)
	}
	var cmd control.Command
	if err := json.Unmarshal(pollRec.Body.Bytes(), &cmd); err != nil {
		t.Fatalf("decode command: %v", err)
	}
	if cmd.Tool != "project_list" || cmd.ID == "" {
		t.Fatalf("unexpected command: %+v", cmd)
	}
	resBody, _ := json.Marshal(control.Result{ID: cmd.ID, Result: json.RawMessage(`[{"id":"p1","title":"Demo"}]`)})
	resRec := httptest.NewRecorder()
	h.ServeHTTP(resRec, authedReq(http.MethodPost, "/control/result", resBody))
	if resRec.Code != http.StatusNoContent {
		t.Fatalf("control/result status %d", resRec.Code)
	}

	callRec := <-done
	if callRec.Code != http.StatusOK || !strings.Contains(callRec.Body.String(), "Demo") {
		t.Fatalf("mcp/call did not receive renderer result: %d %s", callRec.Code, callRec.Body.String())
	}
}

func TestAppInfoReportsThisBuild(t *testing.T) {
	h := newTestServer()
	body, _ := json.Marshal(map[string]any{"tool": "app_info", "args": map[string]string{}})
	req := authedReq(http.MethodPost, "/mcp/call", body)
	// The bridge stamps itself on every call; app_info must hand that straight back so
	// a stale phmcp beside a fresh sidecar is visible.
	req.Header.Set(mcpClientHeader, `{"version":"9.9.9","commit":"deadbeef","dirty":true}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("app_info status %d body=%s", rec.Code, rec.Body.String())
	}

	var got struct {
		Build struct {
			Phd   map[string]any `json:"phd"`
			Phmcp map[string]any `json:"phmcp"`
		} `json:"build"`
		Runtime map[string]any `json:"runtime"`
		Paths   map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("app_info json: %v (%s)", err, rec.Body.String())
	}
	if got.Build.Phd["version"] == "" || got.Build.Phd["go_version"] == "" {
		t.Fatalf("sidecar build not identified: %v", got.Build.Phd)
	}
	if got.Build.Phmcp["commit"] != "deadbeef" || got.Build.Phmcp["dirty"] != true {
		t.Fatalf("caller build not reported: %v", got.Build.Phmcp)
	}
	if _, ok := got.Runtime["pid"]; !ok {
		t.Fatalf("runtime section missing pid: %v", got.Runtime)
	}
	if _, ok := got.Paths["endpoint_file"]; !ok {
		t.Fatalf("paths section missing endpoint_file: %v", got.Paths)
	}
}

func TestAppRegisterIsEchoedByAppInfo(t *testing.T) {
	h := newTestServer()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/app", []byte(`{"electron":"38.0.0","packaged":true}`)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("/app status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	body, _ := json.Marshal(map[string]any{"tool": "app_info", "args": map[string]string{}})
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/mcp/call", body))
	if !strings.Contains(rec.Body.String(), `"electron":"38.0.0"`) {
		t.Fatalf("app_info dropped the shell report: %s", rec.Body.String())
	}

	// Garbage must not be able to poison the report.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/app", []byte(`not json`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid /app body accepted: %d", rec.Code)
	}
}
