// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
// Verbatim copy of pipepush's internal/crypto/kdf.go — see ecies.go's package doc.

package ppcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

const (
	argon2Time    = 3
	argon2Memory  = 64 * 1024 // 64 MB
	argon2Threads = 4
	argon2KeyLen  = 32
	saltLen       = 32
)

// DeriveKey derives a 32-byte AES key from a password and salt using Argon2id.
func DeriveKey(password, salt []byte) []byte {
	return argon2.IDKey(password, salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
}

// GenerateSalt generates a cryptographically random 32-byte salt.
func GenerateSalt() ([]byte, error) {
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("generating salt: %w", err)
	}
	return salt, nil
}

// EncryptPrivateKey encrypts a private key with an AES-256-GCM key derived from the password.
// Returns base64url-encoded ciphertext and base64url-encoded salt.
func EncryptPrivateKey(privateKeyBytes []byte, password string) (encryptedB64, saltB64 string, err error) {
	salt, err := GenerateSalt()
	if err != nil {
		return "", "", err
	}

	key := DeriveKey([]byte(password), salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", "", fmt.Errorf("creating AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", fmt.Errorf("creating GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", "", fmt.Errorf("generating nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, privateKeyBytes, nil)

	return base64.RawURLEncoding.EncodeToString(ciphertext),
		base64.RawURLEncoding.EncodeToString(salt),
		nil
}

// DecryptPrivateKey decrypts an encrypted private key using the password and salt.
func DecryptPrivateKey(encryptedB64, saltB64, password string) ([]byte, error) {
	ciphertext, err := base64.RawURLEncoding.DecodeString(encryptedB64)
	if err != nil {
		return nil, fmt.Errorf("decoding ciphertext: %w", err)
	}

	salt, err := base64.RawURLEncoding.DecodeString(saltB64)
	if err != nil {
		return nil, fmt.Errorf("decoding salt: %w", err)
	}

	key := DeriveKey([]byte(password), salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creating AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypting private key (wrong password?): %w", err)
	}

	return plaintext, nil
}
