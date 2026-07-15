// Package secretbox encrypts database secrets with AES-256-GCM. Each value
// carries a random nonce and authentication tag; LoadKey reads the one current
// 32-byte master key from a base64 environment value.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
)

const keySize = 32

// ErrInvalidKey is returned by New when the key length is not keySize.
var ErrInvalidKey = errors.New("secretbox: key must be 32 bytes")

// ErrCiphertextTooShort is returned when the input to Open is smaller
// than the nonce + GCM tag overhead.
var ErrCiphertextTooShort = errors.New("secretbox: ciphertext too short")

// Box encrypts and decrypts byte slices with one fixed master key.
type Box struct {
	aead cipher.AEAD
}

// New constructs a Box bound to a 32-byte master key.
func New(key []byte) (*Box, error) {
	if len(key) != keySize {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secretbox: aes.NewCipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secretbox: cipher.NewGCM: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal returns nonce || ciphertext || tag with a new random nonce.
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secretbox: read nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open returns an error when the value is truncated or fails authentication.
func (b *Box) Open(sealed []byte) ([]byte, error) {
	ns := b.aead.NonceSize()
	if len(sealed) < ns+b.aead.Overhead() {
		return nil, ErrCiphertextTooShort
	}
	nonce, ciphertext := sealed[:ns], sealed[ns:]
	return b.aead.Open(nil, nonce, ciphertext, nil)
}

// LoadKey reads a base64-encoded 32-byte key from the named environment value.
func LoadKey(envVar string) ([]byte, error) {
	raw := os.Getenv(envVar)
	if raw == "" {
		return nil, fmt.Errorf("secretbox: %s is not set", envVar)
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("secretbox: %s is not valid base64: %w", envVar, err)
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("secretbox: %s decodes to %d bytes, expected %d", envVar, len(key), keySize)
	}
	return key, nil
}
