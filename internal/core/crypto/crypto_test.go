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

package crypto

import (
	"bytes"
	"testing"

	pb "github.com/Gerry3010/passbubble/backend/pkg/crypto"
)

// newAccount simulates Passbubble registration: generate keypairs, derive the
// master key from the password, and encrypt the private keys with it — exactly
// what cli/internal/cli/login.go::registerCmd does. Returns the values a login
// response would carry.
func newAccount(t *testing.T, password string) (salt, encPrivX, encPrivM, pubX, pubM []byte) {
	t.Helper()
	privX, pubX, err := pb.GenerateX25519()
	if err != nil {
		t.Fatalf("gen x25519: %v", err)
	}
	privM, pubM, err := pb.GenerateMLKEM768()
	if err != nil {
		t.Fatalf("gen mlkem: %v", err)
	}
	kdf, err := pb.NewKDFParams()
	if err != nil {
		t.Fatalf("kdf params: %v", err)
	}
	master := pb.DeriveKey(password, kdf)
	if encPrivX, err = pb.Encrypt(master, privX); err != nil {
		t.Fatalf("enc privX: %v", err)
	}
	if encPrivM, err = pb.Encrypt(master, privM); err != nil {
		t.Fatalf("enc privM: %v", err)
	}
	return kdf.Salt, encPrivX, encPrivM, pubX, pubM
}

func TestUnlockSealOpenRoundTrip(t *testing.T) {
	const pw = "correct horse battery staple"
	salt, encPrivX, encPrivM, pubX, pubM := newAccount(t, pw)

	keys, err := Unlock(pw, salt, 0, 0, "user-1", encPrivX, encPrivM, pubX, pubM)
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}

	payload := []byte(`{"title":"Mein Projekt","body":"geheime Notiz"}`)
	encData, encKey, err := keys.Seal(payload)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(encData, []byte("geheime")) {
		t.Fatal("ciphertext leaks plaintext")
	}

	got, err := keys.Open(encData, encKey)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round-trip mismatch: got %q", got)
	}
}

func TestWrongPasswordFails(t *testing.T) {
	salt, encPrivX, encPrivM, pubX, pubM := newAccount(t, "right-password")
	if _, err := Unlock("wrong-password", salt, 0, 0, "u", encPrivX, encPrivM, pubX, pubM); err == nil {
		t.Fatal("expected unlock to fail with wrong password")
	}
}

// TestWireCompatWithRawPassbubble proves a payload sealed by ProjectHub decrypts
// with the raw Passbubble primitives (and vice-versa) — i.e. ProjectHub entries
// are byte-compatible with what the Passbubble apps produce and consume.
func TestWireCompatWithRawPassbubble(t *testing.T) {
	const pw = "interop"
	salt, encPrivX, encPrivM, pubX, pubM := newAccount(t, pw)
	keys, err := Unlock(pw, salt, 0, 0, "u", encPrivX, encPrivM, pubX, pubM)
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}

	payload := []byte("hello passbubble")
	encData, encKey, err := keys.Seal(payload)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	// Decrypt using ONLY raw passbubble functions, the way the backend/CLI does.
	dataKey, err := pb.DecryptDataKey(encKey, keys.PrivX25519, keys.PrivMLKEM)
	if err != nil {
		t.Fatalf("raw DecryptDataKey: %v", err)
	}
	got, err := pb.Decrypt(dataKey, encData)
	if err != nil {
		t.Fatalf("raw Decrypt: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("interop mismatch: got %q", got)
	}
}
