// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package pipepush

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSendWebhook(t *testing.T) {
	var gotPath string
	var gotBody WebhookRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req := WebhookRequest{
		Token:    "pp_test",
		Status:   StatusSuccess,
		Pipeline: "Deploy",
		Message:  "hello",
	}
	// baseURL with a trailing slash must still resolve to /api/webhook exactly once.
	if err := SendWebhook(context.Background(), srv.URL+"/", req); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/webhook" {
		t.Errorf("path = %q, want /api/webhook", gotPath)
	}
	if gotBody.Token != "pp_test" || gotBody.Status != StatusSuccess || gotBody.Pipeline != "Deploy" {
		t.Errorf("decoded body mismatch: %+v", gotBody)
	}
}

func TestSendWebhookRejectsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad token", http.StatusUnauthorized)
	}))
	defer srv.Close()

	err := SendWebhook(context.Background(), srv.URL, WebhookRequest{Token: "x", Status: StatusRunning})
	if err == nil {
		t.Fatal("expected error on 401 response, got nil")
	}
}

func TestSendWebhookValidates(t *testing.T) {
	if err := SendWebhook(context.Background(), "http://x", WebhookRequest{Status: StatusRunning}); err == nil {
		t.Error("expected error for missing token")
	}
	if err := SendWebhook(context.Background(), "http://x", WebhookRequest{Token: "pp_x"}); err == nil {
		t.Error("expected error for missing status")
	}
}
