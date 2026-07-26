// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package nativeserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFileWriteThenRead(t *testing.T) {
	h := newTestServer()
	path := filepath.Join(t.TempDir(), "hello.txt")

	// write
	body, _ := json.Marshal(map[string]string{"path": path, "content": "hallo welt"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/file", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("write status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := os.ReadFile(path); string(got) != "hallo welt" {
		t.Fatalf("file content = %q", got)
	}

	// read it back through the handler
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodGet, "/file?path="+path, nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "hallo welt" {
		t.Fatalf("read-back status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestFileWriteRejectsRelativePath(t *testing.T) {
	h := newTestServer()
	body, _ := json.Marshal(map[string]string{"path": "relative/nope.txt", "content": "x"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/file", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("relative path should be 400, got %d", rec.Code)
	}
}

func TestDirListAndMkdir(t *testing.T) {
	h := newTestServer()
	root := t.TempDir()
	// seed: a subdir and two files
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "b.txt"), []byte("bb"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodGet, "/dir?path="+root, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("dir status %d", rec.Code)
	}
	var entries []struct {
		Name  string `json:"name"`
		IsDir bool   `json:"is_dir"`
		Size  int64  `json:"size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// folders first, then files alphabetically → sub, a.txt, b.txt
	if len(entries) != 3 || !entries[0].IsDir || entries[0].Name != "sub" ||
		entries[1].Name != "a.txt" || entries[2].Name != "b.txt" {
		t.Fatalf("unexpected listing: %+v", entries)
	}
	if entries[2].Size != 2 {
		t.Fatalf("b.txt size = %d", entries[2].Size)
	}

	// mkdir creates nested dirs
	newDir := filepath.Join(root, "x", "y")
	body, _ := json.Marshal(map[string]string{"path": newDir})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/mkdir", body))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("mkdir status %d", rec.Code)
	}
	if fi, err := os.Stat(newDir); err != nil || !fi.IsDir() {
		t.Fatalf("mkdir did not create dir: %v", err)
	}
}

func TestFileMove(t *testing.T) {
	h := newTestServer()
	root := t.TempDir()
	src := filepath.Join(root, "a.txt")
	_ = os.WriteFile(src, []byte("x"), 0o644)
	dst := filepath.Join(root, "sub", "a.txt")
	_ = os.MkdirAll(filepath.Join(root, "sub"), 0o755)

	body, _ := json.Marshal(map[string]string{"src": src, "dst": dst})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/move", body))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("move status %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("src still exists")
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("dst missing: %v", err)
	}
	// refuse to overwrite existing dst
	_ = os.WriteFile(src, []byte("y"), 0o644)
	body, _ = json.Marshal(map[string]string{"src": src, "dst": dst})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/move", body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("overwrite should be 409, got %d", rec.Code)
	}
}

func TestFileWriteRefusesDirectory(t *testing.T) {
	h := newTestServer()
	dir := t.TempDir()
	body, _ := json.Marshal(map[string]string{"path": dir, "content": "x"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/file", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("writing over a dir should be 400, got %d", rec.Code)
	}
}
