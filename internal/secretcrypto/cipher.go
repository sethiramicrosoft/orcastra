package secretcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
)

type Cipher struct {
	aead cipher.AEAD
	kid  string
}

func New(encryptionKeyB64, keyID, fallbackSeed string) (*Cipher, error) {
	keyBytes, err := resolveKey(encryptionKeyB64, fallbackSeed)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("create aes block: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create aes-gcm: %w", err)
	}
	kid := strings.TrimSpace(keyID)
	if kid == "" {
		kid = "aesgcm-v1"
	}
	return &Cipher{aead: aead, kid: kid}, nil
}

func (c *Cipher) KeyID() string {
	return c.kid
}

func (c *Cipher) EncryptString(v string) ([]byte, string, error) {
	out, err := c.Encrypt([]byte(v))
	if err != nil {
		return nil, "", err
	}
	return out, c.kid, nil
}

func (c *Cipher) Encrypt(plain []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := c.aead.Seal(nil, nonce, plain, nil)
	return append(nonce, ciphertext...), nil
}

func (c *Cipher) Decrypt(blob []byte) ([]byte, error) {
	n := c.aead.NonceSize()
	if len(blob) < n {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce := blob[:n]
	ciphertext := blob[n:]
	plain, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plain, nil
}

func resolveKey(encryptionKeyB64, fallbackSeed string) ([]byte, error) {
	keyB64 := strings.TrimSpace(encryptionKeyB64)
	if keyB64 != "" {
		key, err := base64.StdEncoding.DecodeString(keyB64)
		if err != nil {
			return nil, fmt.Errorf("decode ENCRYPTION_KEY_B64: %w", err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("ENCRYPTION_KEY_B64 must decode to 32 bytes")
		}
		return key, nil
	}
	if strings.TrimSpace(fallbackSeed) == "" {
		return nil, fmt.Errorf("encryption key is not configured")
	}
	sum := sha256.Sum256([]byte(fallbackSeed))
	key := make([]byte, 32)
	copy(key, sum[:])
	return key, nil
}
