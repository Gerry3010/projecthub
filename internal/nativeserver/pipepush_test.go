// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package nativeserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPipepushLoginRelay(t *testing.T) {
	var gotBody map[string]string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/auth/login" {
			t.Fatalf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"jwt": "fake-jwt", "publicKey": "pk", "encryptedPrivateKey": "epk", "kdfSalt": "salt"})
	}))
	defer upstream.Close()

	h := newTestServer()
	body, _ := json.Marshal(map[string]string{"base": upstream.URL, "email": "a@b.com", "password": "pw"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/pipepush/login", body))

	if rec.Code != 200 {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if gotBody["email"] != "a@b.com" || gotBody["password"] != "pw" {
		t.Fatalf("upstream didn't receive expected credentials: %+v", gotBody)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["jwt"] != "fake-jwt" {
		t.Fatalf("unexpected relayed response: %+v", resp)
	}
}

func TestPipepushLoginRejectsBadInput(t *testing.T) {
	h := newTestServer()

	cases := []string{
		`{"base":"not-a-url","email":"a@b.com","password":"pw"}`,
		`{"base":"http://x","email":"","password":"pw"}`,
		`{"base":"http://x","email":"a@b.com","password":""}`,
		`{"base":"ftp://x","email":"a@b.com","password":"pw"}`,
	}
	for _, body := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, authedReq(http.MethodPost, "/pipepush/login", []byte(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: want 400, got %d", body, rec.Code)
		}
	}
}

func TestPipepushPipelinesRelay(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/projects/proj-1/pipelines" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"pl-1","projectId":"proj-1","encryptedName":"enc"}]`))
	}))
	defer upstream.Close()

	h := newTestServer()
	req := authedReq(http.MethodGet, "/pipepush/pipelines?base="+upstream.URL+"&project=proj-1", nil)
	req.Header.Set("X-PP-Auth", "target-jwt")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer target-jwt" {
		t.Fatalf("X-PP-Auth wasn't mapped to upstream Authorization: got %q", gotAuth)
	}
	if !strings.Contains(rec.Body.String(), `"id":"pl-1"`) {
		t.Fatalf("unexpected relayed body: %s", rec.Body.String())
	}
}

func TestPipepushRunsRelay(t *testing.T) {
	var gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		if r.URL.Path != "/api/pipelines/pl-1/runs" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"run-1","status":"success"}]`))
	}))
	defer upstream.Close()

	h := newTestServer()
	req := authedReq(http.MethodGet, "/pipepush/runs?base="+upstream.URL+"&pipeline=pl-1&limit=25", nil)
	req.Header.Set("X-PP-Auth", "target-jwt")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if gotQuery != "limit=25" {
		t.Fatalf("expected limit forwarded, got query %q", gotQuery)
	}
	if !strings.Contains(rec.Body.String(), `"status":"success"`) {
		t.Fatalf("unexpected relayed body: %s", rec.Body.String())
	}
}

func TestPipepushRelayPassesThroughUpstreamErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid credentials"}`))
	}))
	defer upstream.Close()

	h := newTestServer()
	body, _ := json.Marshal(map[string]string{"base": upstream.URL, "email": "a@b.com", "password": "wrong"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authedReq(http.MethodPost, "/pipepush/login", body))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 passed through from upstream, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid credentials") {
		t.Fatalf("expected upstream error body relayed, got %s", rec.Body.String())
	}
}
