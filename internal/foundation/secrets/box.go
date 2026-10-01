// Package secrets encrypts platform-owned credentials with a deployment key.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

type Box struct{ aead cipher.AEAD }

func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("deployment secret key must contain 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Seal(scope string, value []byte) ([]byte, error) {
	if b == nil || scope == "" {
		return nil, errors.New("secret encryption is not configured")
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, value, []byte(scope)), nil
}

func (b *Box) Open(scope string, value []byte) ([]byte, error) {
	if b == nil || scope == "" || len(value) < b.aead.NonceSize() {
		return nil, errors.New("invalid encrypted secret")
	}
	n := b.aead.NonceSize()
	return b.aead.Open(nil, value[:n], value[n:], []byte(scope))
}
