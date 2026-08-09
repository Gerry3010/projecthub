// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.

// Package ppcrypto is a verbatim copy of the pipepush server's own
// internal/crypto package (github.com/Gerry3010/pipepush, same author/license),
// vendored here because it lives in a sibling repo's unexported internal/ tree
// and Go modules can't import across that boundary. It implements pipepush's
// wire crypto: ECIES (ephemeral X25519 + HKDF-SHA256 + XChaCha20-Poly1305) for
// end-to-end-encrypted payloads, and an Argon2id-derived AES-256-GCM wrapper for
// the user's private key at rest. ProjectHub only ever *decrypts* with it (the
// pipepush tile reading run payloads with the user's own key) — it never
// originates pipepush data, so Encrypt/EncryptPrivateKey exist here only to
// make round-trip tests possible, not because production code calls them.
package ppcrypto

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// Encrypt encrypts plaintext for the given recipient public key using ECIES:
// ephemeral X25519 key + HKDF-SHA256 + XChaCha20-Poly1305.
//
// Wire format: [32 ephemeral pubkey][24 nonce][ciphertext+16 tag]
// Returns base64url-encoded ciphertext.
func Encrypt(recipientPub *ecdh.PublicKey, plaintext []byte) (string, error) {
	// Generate ephemeral key pair
	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generating ephemeral key: %w", err)
	}

	// ECDH shared secret
	shared, err := ephemeral.ECDH(recipientPub)
	if err != nil {
		return "", fmt.Errorf("ECDH: %w", err)
	}

	// Derive symmetric key via HKDF-SHA256
	symKey, err := deriveSymKey(shared, ephemeral.PublicKey().Bytes(), recipientPub.Bytes())
	if err != nil {
		return "", err
	}

	// XChaCha20-Poly1305 (24-byte nonce — random nonce is safe)
	aead, err := chacha20poly1305.NewX(symKey)
	if err != nil {
		return "", fmt.Errorf("creating AEAD: %w", err)
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}

	ciphertext := aead.Seal(nil, nonce, plaintext, nil)

	// Wire format: ephemeral_pub || nonce || ciphertext
	wire := make([]byte, 0, 32+len(nonce)+len(ciphertext))
	wire = append(wire, ephemeral.PublicKey().Bytes()...)
	wire = append(wire, nonce...)
	wire = append(wire, ciphertext...)

	return base64.RawURLEncoding.EncodeToString(wire), nil
}

// Decrypt decrypts a base64url-encoded ciphertext using the recipient's private key.
func Decrypt(recipientPriv *ecdh.PrivateKey, ciphertextB64 string) ([]byte, error) {
	wire, err := base64.RawURLEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return nil, fmt.Errorf("decoding ciphertext: %w", err)
	}

	if len(wire) < 32+chacha20poly1305.NonceSizeX {
		return nil, fmt.Errorf("ciphertext too short")
	}

	ephemeralPubBytes := wire[:32]
	nonce := wire[32 : 32+chacha20poly1305.NonceSizeX]
	ciphertext := wire[32+chacha20poly1305.NonceSizeX:]

	ephemeralPub, err := ecdh.X25519().NewPublicKey(ephemeralPubBytes)
	if err != nil {
		return nil, fmt.Errorf("parsing ephemeral public key: %w", err)
	}

	// ECDH shared secret
	shared, err := recipientPriv.ECDH(ephemeralPub)
	if err != nil {
		return nil, fmt.Errorf("ECDH: %w", err)
	}

	symKey, err := deriveSymKey(shared, ephemeralPubBytes, recipientPriv.PublicKey().Bytes())
	if err != nil {
		return nil, err
	}

	aead, err := chacha20poly1305.NewX(symKey)
	if err != nil {
		return nil, fmt.Errorf("creating AEAD: %w", err)
	}

	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypting payload: %w", err)
	}

	return plaintext, nil
}

// EncryptString encrypts a string and returns base64url ciphertext.
func EncryptString(recipientPub *ecdh.PublicKey, plaintext string) (string, error) {
	return Encrypt(recipientPub, []byte(plaintext))
}

// DecryptString decrypts a base64url ciphertext and returns the plaintext string.
func DecryptString(recipientPriv *ecdh.PrivateKey, ciphertextB64 string) (string, error) {
	b, err := Decrypt(recipientPriv, ciphertextB64)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func deriveSymKey(sharedSecret, ephemeralPub, recipientPub []byte) ([]byte, error) {
	// HKDF-SHA256 with context as info. Build info into a fresh slice — never
	// append into the caller's slice, which may alias the ciphertext buffer.
	info := make([]byte, 0, len(ephemeralPub)+len(recipientPub))
	info = append(info, ephemeralPub...)
	info = append(info, recipientPub...)
	reader := hkdf.New(sha256.New, sharedSecret, []byte("pipepush-ecies-v1"), info)

	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, fmt.Errorf("deriving symmetric key: %w", err)
	}
	return key, nil
}
