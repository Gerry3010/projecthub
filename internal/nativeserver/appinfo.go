// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// app_info answers "which ProjectHub is running right now, and what is it doing?".
// It deliberately knows nothing about the source repo: it reports what this build IS
// (version, commit, dirty flag, asset hashes) and leaves the comparison against a
// checkout to whoever asks — that keeps the sidecar honest and repo-independent.

package nativeserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Gerry3010/projecthub/internal/buildinfo"
	"github.com/Gerry3010/projecthub/internal/discovery"
	"github.com/Gerry3010/projecthub/internal/local"
	"github.com/Gerry3010/projecthub/internal/ptyhost"
)

// appReport is the app_info payload. Field names are the tool's contract.
type appReport struct {
	Build   buildReport       `json:"build"`
	App     json.RawMessage   `json:"app,omitempty"` // last report from the Electron shell
	Runtime runtimeReport     `json:"runtime"`
	Paths   map[string]string `json:"paths"`
}

// buildReport identifies the three pieces that can drift apart in a half-finished
// update: the sidecar, the MCP bridge that called us, and the WASM bundle on disk.
type buildReport struct {
	Phd   buildinfo.Info  `json:"phd"`
	Phmcp *buildinfo.Info `json:"phmcp,omitempty"` // stamped by the caller, if it sent one
	Wasm  *fileStamp      `json:"wasm,omitempty"`
}

// fileStamp identifies a shipped asset well enough to compare two installs.
type fileStamp struct {
	Path     string `json:"path"`
	SHA256   string `json:"sha256"` // first 12 hex chars — enough to tell builds apart
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
}

type runtimeReport struct {
	PID              int                   `json:"pid"`
	PPID             int                   `json:"ppid"`
	UptimeS          int64                 `json:"uptime_s"`
	Port             int                   `json:"port"`
	Ptys             []ptyhost.SessionInfo `json:"ptys"`
	PassbubbleURL    string                `json:"passbubble_url,omitempty"`
	BackendReachable bool                  `json:"backend_reachable"`
}

// SetRuntimeInfo tells the native API where it is listening and which directory it
// serves the frontend from — both only known to the process that started it.
func (s *Server) SetRuntimeInfo(port int, webDir string) {
	s.port, s.webDir = port, webDir
}

// appRegister receives the Electron shell's self-report (versions, bundle paths, the
// open windows). The shell re-posts it whenever a window opens or closes, so app_info
// always reflects the current window set. Body is stored verbatim.
func (s *Server) appRegister(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil || !json.Valid(body) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.appReport = append(json.RawMessage(nil), body...)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// appInfo assembles the report. Everything is best-effort: a missing piece is left
// out rather than failing the call, because the tool's whole point is to work even
// when part of the app is unhealthy.
func (s *Server) appInfo(clientBuild *buildinfo.Info) appReport {
	s.mu.Lock()
	appJSON := s.appReport
	port, webDir := s.port, s.webDir
	s.mu.Unlock()

	rep := appReport{
		Build: buildReport{Phd: buildinfo.Get(), Phmcp: clientBuild},
		App:   appJSON,
		Runtime: runtimeReport{
			PID:     os.Getpid(),
			PPID:    os.Getppid(),
			UptimeS: int64(time.Since(s.started).Seconds()),
			Port:    port,
		},
		Paths: map[string]string{},
	}
	if s.pty != nil {
		rep.Runtime.Ptys = s.pty.List()
	}
	if rep.Runtime.Ptys == nil {
		rep.Runtime.Ptys = []ptyhost.SessionInfo{}
	}
	if s.getServer != nil {
		rep.Runtime.PassbubbleURL = s.getServer()
		rep.Runtime.BackendReachable = reachable(rep.Runtime.PassbubbleURL)
	}
	if webDir != "" {
		rep.Build.Wasm = stampFile(filepath.Join(webDir, "app.wasm"))
		rep.Paths["web_dir"] = webDir
	}
	if p, err := discovery.Path(); err == nil {
		rep.Paths["endpoint_file"] = p
		rep.Paths["config_dir"] = filepath.Dir(p)
		rep.Paths["server_url_file"] = filepath.Join(filepath.Dir(p), "server.url")
	}
	if p := local.PhmcpPath(); p != "" {
		rep.Paths["phmcp"] = p
	}
	if p := local.ClaudeBin(); p != "" {
		rep.Paths["claude_bin"] = p
	}
	if p, err := os.Executable(); err == nil {
		rep.Paths["phd"] = p
	}
	return rep
}

// stampFile hashes a shipped asset. Returns nil when it isn't there — a hosted build
// serves the frontend from elsewhere, and that is not an error.
func stampFile(path string) *fileStamp {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil
	}
	return &fileStamp{
		Path:     path,
		SHA256:   hex.EncodeToString(h.Sum(nil))[:12],
		Size:     fi.Size(),
		Modified: fi.ModTime().Format(time.RFC3339),
	}
}

// reachable does one short GET against the Passbubble upstream: any answer counts,
// because we only want to know whether something is listening.
func reachable(url string) bool {
	if url == "" {
		return false
	}
	c := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := c.Get(url)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}
