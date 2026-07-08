// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
// Verbatim copy of pipepush's internal/crypto/keygen.go — see ecies.go's package doc.

package ppcrypto

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// KeyPair holds an X25519 key pair.
type KeyPair struct {
	PrivateKey *ecdh.PrivateKey
	PublicKey  *ecdh.PublicKey
}

// GenerateKeyPair generates a new X25519 key pair.
func GenerateKeyPair() (*KeyPair, error) {
	curve := ecdh.X25519()
	priv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating X25519 key pair: %w", err)
	}
	return &KeyPair{
		PrivateKey: priv,
		PublicKey:  priv.PublicKey(),
	}, nil
}

// PublicKeyToBase64 encodes a public key to base64url.
func PublicKeyToBase64(pub *ecdh.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(pub.Bytes())
}

// PrivateKeyToBase64 encodes a private key's raw bytes to base64url.
func PrivateKeyToBase64(priv *ecdh.PrivateKey) string {
	return base64.RawURLEncoding.EncodeToString(priv.Bytes())
}

// PrivateKeyBytesToBase64 encodes raw private key bytes to base64url.
func PrivateKeyBytesToBase64(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// PublicKeyFromBase64 decodes a base64url-encoded X25519 public key.
func PublicKeyFromBase64(s string) (*ecdh.PublicKey, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decoding public key: %w", err)
	}
	pub, err := ecdh.X25519().NewPublicKey(b)
	if err != nil {
		return nil, fmt.Errorf("parsing public key: %w", err)
	}
	return pub, nil
}

// PrivateKeyFromBytes constructs an X25519 private key from raw bytes.
func PrivateKeyFromBytes(b []byte) (*ecdh.PrivateKey, error) {
	priv, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("parsing private key bytes: %w", err)
	}
	return priv, nil
}

// PrivateKeyFromBase64 decodes a base64url-encoded X25519 private key.
func PrivateKeyFromBase64(s string) (*ecdh.PrivateKey, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decoding private key: %w", err)
	}
	priv, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("parsing private key: %w", err)
	}
	return priv, nil
}
