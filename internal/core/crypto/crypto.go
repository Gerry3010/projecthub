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

// Package crypto wraps Passbubble's E2E crypto (Argon2id KDF + X25519/ML-KEM-768
// hybrid KEM + AES-256-GCM) into a session-oriented API for ProjectHub. It reuses
// the public passbubble backend/pkg/crypto package unchanged, so ProjectHub stays
// wire-compatible with the Passbubble apps. This package is WASM-safe: it imports
// only pure-Go crypto (CIRCL + golang.org/x/crypto), no server/DB dependencies.
package crypto

import (
	"errors"

	pb "github.com/Gerry3010/passbubble/backend/pkg/crypto"
)

// Keys holds a user's decrypted key material. It lives only in memory (the WASM
// heap in the browser, or the TUI process) and is never persisted or sent to a
// server.
type Keys struct {
	UserID     string
	PrivX25519 []byte
	PrivMLKEM  []byte
	PubX25519  []byte
	PubMLKEM   []byte
}

// Unlock derives the master key from the master password (Argon2id with the
// account's stored KDF params) and decrypts the user's private keys. All byte
// inputs are the raw (already base64-decoded) values from the login response.
func Unlock(masterPassword string, salt []byte, kdfTime, kdfMemory uint32, userID string,
	encPrivX25519, encPrivMLKEM, pubX25519, pubMLKEM []byte) (*Keys, error) {

	if kdfTime == 0 {
		kdfTime = pb.Argon2DefaultTime
	}
	if kdfMemory == 0 {
		kdfMemory = pb.Argon2DefaultMemory
	}
	masterKey := pb.DeriveKey(masterPassword, &pb.KDFParams{Salt: salt, Time: kdfTime, Memory: kdfMemory})

	privX, err := pb.Decrypt(masterKey, encPrivX25519)
	if err != nil {
		return nil, errors.New("unlock: wrong master password or corrupt x25519 key")
	}
	privM, err := pb.Decrypt(masterKey, encPrivMLKEM)
	if err != nil {
		return nil, errors.New("unlock: wrong master password or corrupt mlkem key")
	}
	return &Keys{
		UserID:     userID,
		PrivX25519: privX,
		PrivMLKEM:  privM,
		PubX25519:  pubX25519,
		PubMLKEM:   pubMLKEM,
	}, nil
}

// Seal encrypts a plaintext payload for the owner only: it generates a random
// data key, AES-256-GCM-encrypts the payload, and wraps the data key for the
// owner via the hybrid KEM. Returns the nonce||ciphertext blob and the wrapped
// data key for this user, matching Passbubble's entry storage format.
func (k *Keys) Seal(plaintext []byte) (encData []byte, encKeyForSelf []byte, err error) {
	dataKey, err := pb.RandKey()
	if err != nil {
		return nil, nil, err
	}
	encData, err = pb.Encrypt(dataKey, plaintext)
	if err != nil {
		return nil, nil, err
	}
	encKeyForSelf, err = pb.EncryptDataKey(dataKey, k.PubX25519, k.PubMLKEM)
	if err != nil {
		return nil, nil, err
	}
	return encData, encKeyForSelf, nil
}

// Open reverses Seal: it unwraps the data key with the user's private keys and
// decrypts the payload. encKeyForSelf is the EntryKey addressed to this user.
func (k *Keys) Open(encData, encKeyForSelf []byte) ([]byte, error) {
	dataKey, err := pb.DecryptDataKey(encKeyForSelf, k.PrivX25519, k.PrivMLKEM)
	if err != nil {
		return nil, err
	}
	return pb.Decrypt(dataKey, encData)
}
