package auth_test

import (
	"testing"

	"github.com/sethiramicrosoft/orcastra/internal/auth"
)

func TestHashAndVerify(t *testing.T) {
	password := "correct-horse-battery-staple"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "" {
		t.Fatal("empty hash returned")
	}

	ok, err := auth.VerifyPassword(hash, password)
	if err != nil {
		t.Errorf("VerifyPassword correct password error: %v", err)
	}
	if !ok {
		t.Error("VerifyPassword correct password: expected true")
	}

	ok2, err2 := auth.VerifyPassword(hash, "wrong-password")
	if err2 != nil {
		t.Logf("VerifyPassword wrong password error (ok): %v", err2)
	}
	if ok2 {
		t.Error("VerifyPassword wrong password: expected false")
	}
}

func TestHashUniqueness(t *testing.T) {
	h1, _ := auth.HashPassword("same-password")
	h2, _ := auth.HashPassword("same-password")
	if h1 == h2 {
		t.Error("two hashes of the same password should differ (salt)")
	}
}

func TestPasswordTooShort(t *testing.T) {
	_, err := auth.HashPassword("short")
	if err == nil {
		t.Error("expected error for short password, got nil")
	}
}
