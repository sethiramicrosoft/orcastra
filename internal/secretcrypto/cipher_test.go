package secretcrypto

import "testing"

func TestEncryptDecryptRoundTrip(t *testing.T) {
	c, err := New("", "aesgcm-v1", "fallback-seed")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	plain := []byte("hello-secret-value")
	ct, err := c.Encrypt(plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if len(ct) <= len(plain) {
		t.Fatalf("ciphertext should include nonce+tag, got len=%d", len(ct))
	}
	got, err := c.Decrypt(ct)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("decrypted mismatch: got %q want %q", string(got), string(plain))
	}
}

func TestNewWithInvalidBase64Key(t *testing.T) {
	_, err := New("not-base64", "aesgcm-v1", "")
	if err == nil {
		t.Fatal("expected error for invalid base64 key")
	}
}
