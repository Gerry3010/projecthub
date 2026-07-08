// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
// Ported from pipepush's internal/crypto/crypto_test.go to confirm this vendored
// copy stays wire-compatible with the real pipepush server/CLI.

package ppcrypto_test

import (
	"testing"

	"github.com/Gerry3010/projecthub/internal/pipepush/ppcrypto"
)

func TestECIESRoundTrip(t *testing.T) {
	alice, err := ppcrypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	plaintext := "Hello, pipepush! Status: success, branch: main, commit: abc123def456"

	ciphertext, err := ppcrypto.EncryptString(alice.PublicKey, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	decrypted, err := ppcrypto.DecryptString(alice.PrivateKey, ciphertext)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if decrypted != plaintext {
		t.Errorf("expected %q, got %q", plaintext, decrypted)
	}
}

func TestECIESDifferentKeyFails(t *testing.T) {
	alice, _ := ppcrypto.GenerateKeyPair()
	bob, _ := ppcrypto.GenerateKeyPair()

	ciphertext, _ := ppcrypto.EncryptString(alice.PublicKey, "secret")
	if _, err := ppcrypto.DecryptString(bob.PrivateKey, ciphertext); err == nil {
		t.Error("expected decryption with wrong key to fail")
	}
}

func TestKeySerializationRoundTrip(t *testing.T) {
	kp, err := ppcrypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}

	privB64 := ppcrypto.PrivateKeyToBase64(kp.PrivateKey)
	priv2, err := ppcrypto.PrivateKeyFromBase64(privB64)
	if err != nil {
		t.Fatalf("parsing private key: %v", err)
	}

	pubB64 := ppcrypto.PublicKeyToBase64(kp.PublicKey)
	if _, err := ppcrypto.PublicKeyFromBase64(pubB64); err != nil {
		t.Fatalf("parsing public key: %v", err)
	}

	// Encrypt with original public key, decrypt with restored private key
	ct, err := ppcrypto.EncryptString(kp.PublicKey, "test")
	if err != nil {
		t.Fatal(err)
	}
	pt, err := ppcrypto.DecryptString(priv2, ct)
	if err != nil {
		t.Fatalf("decrypt with restored key: %v", err)
	}
	if pt != "test" {
		t.Error("round-trip failed")
	}
}

func TestKDFPrivateKeyRoundTrip(t *testing.T) {
	kp, _ := ppcrypto.GenerateKeyPair()
	privBytes := kp.PrivateKey.Bytes()
	password := "hunter2-super-secret"

	encrypted, salt, err := ppcrypto.EncryptPrivateKey(privBytes, password)
	if err != nil {
		t.Fatal(err)
	}

	decrypted, err := ppcrypto.DecryptPrivateKey(encrypted, salt, password)
	if err != nil {
		t.Fatal(err)
	}

	if string(decrypted) != string(privBytes) {
		t.Error("private key round-trip failed")
	}
}

func TestKDFWrongPasswordFails(t *testing.T) {
	kp, _ := ppcrypto.GenerateKeyPair()
	encrypted, salt, _ := ppcrypto.EncryptPrivateKey(kp.PrivateKey.Bytes(), "correct-password")

	if _, err := ppcrypto.DecryptPrivateKey(encrypted, salt, "wrong-password"); err == nil {
		t.Error("expected decryption with wrong password to fail")
	}
}
