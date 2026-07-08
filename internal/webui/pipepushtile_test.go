// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

package webui

import (
	"testing"

	"github.com/Gerry3010/projecthub/internal/pipepush"
	"github.com/Gerry3010/projecthub/internal/pipepush/ppcrypto"
)

func TestDecryptRunRoundtrip(t *testing.T) {
	kp, err := ppcrypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"status":"success","branch":"main","commit":"abc123def456","message":"deployed"}`
	ct, err := ppcrypto.EncryptString(kp.PublicKey, payload)
	if err != nil {
		t.Fatal(err)
	}

	run := decryptRun(kp.PrivateKey, pipepush.PPRun{ID: "r1", Status: "success", EncryptedPayload: ct})
	if run.DecodeErr {
		t.Fatal("expected successful decrypt")
	}
	if run.Payload.Branch != "main" || run.Payload.Commit != "abc123def456" || run.Payload.Message != "deployed" {
		t.Fatalf("unexpected decrypted payload: %+v", run.Payload)
	}
}

func TestDecryptRunWrongKeyFails(t *testing.T) {
	alice, _ := ppcrypto.GenerateKeyPair()
	bob, _ := ppcrypto.GenerateKeyPair()
	ct, err := ppcrypto.EncryptString(alice.PublicKey, `{"status":"success"}`)
	if err != nil {
		t.Fatal(err)
	}

	run := decryptRun(bob.PrivateKey, pipepush.PPRun{ID: "r1", EncryptedPayload: ct})
	if !run.DecodeErr {
		t.Fatal("expected decode error with the wrong key")
	}
}

func TestRunSummary(t *testing.T) {
	cases := []struct {
		name string
		run  ppRun
		want string
	}{
		{"branch+commit", ppRun{Payload: pipepush.RunPayload{Branch: "main", Commit: "abcdef1234567"}}, "main · abcdef12"},
		{"branch only", ppRun{Payload: pipepush.RunPayload{Branch: "main"}}, "main"},
		{"commit only", ppRun{Payload: pipepush.RunPayload{Commit: "abcdef12"}}, "abcdef12"},
		{"decode error", ppRun{DecodeErr: true, Payload: pipepush.RunPayload{Branch: "main"}}, ""},
	}
	for _, c := range cases {
		if got := runSummary(c.run); got != c.want {
			t.Errorf("%s: runSummary() = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStatusIcon(t *testing.T) {
	if statusIcon(pipepush.StatusSuccess) == statusIcon(pipepush.StatusFailure) {
		t.Error("expected distinct icons for success vs failure")
	}
	if statusIcon("unknown-status") == "" {
		t.Error("expected a fallback icon for unknown status")
	}
}
