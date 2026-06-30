package auth_test

import (
	"testing"
	"time"

	"github.com/sethiramicrosoft/orcastra/internal/auth"
)

func TestJWTSignAndParse(t *testing.T) {
	signer, err := auth.NewJWTSigner("super-secret-key-at-least-32-chars!!", "orcastra-test")
	if err != nil {
		t.Fatalf("NewJWTSigner: %v", err)
	}

	userID := "user-uuid-123"
	teamID := "team-uuid-456"

	token, err := signer.Sign(userID, teamID, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if token == "" {
		t.Fatal("empty token returned")
	}

	claims, err := signer.Parse(token)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if claims.UserID != userID {
		t.Errorf("UserID mismatch: got %q want %q", claims.UserID, userID)
	}
	if claims.TeamID != teamID {
		t.Errorf("TeamID mismatch: got %q want %q", claims.TeamID, teamID)
	}
}

func TestJWTExpiry(t *testing.T) {
	signer, _ := auth.NewJWTSigner("super-secret-key-at-least-32-chars!!", "orcastra-test")
	token, _ := signer.Sign("u", "t", -time.Second)
	_, err := signer.Parse(token)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
}

func TestJWTInvalidSecret(t *testing.T) {
	signerA, _ := auth.NewJWTSigner("secret-A-32-chars-long-minimum!!", "orcastra-test")
	signerB, _ := auth.NewJWTSigner("secret-B-32-chars-long-minimum!!", "orcastra-test")

	token, _ := signerA.Sign("u", "t", time.Hour)
	_, err := signerB.Parse(token)
	if err == nil {
		t.Fatal("expected error when parsing with wrong secret, got nil")
	}
}

func TestJWTEmptySecret(t *testing.T) {
	_, err := auth.NewJWTSigner("", "orcastra-test")
	if err == nil {
		t.Fatal("expected error for empty secret, got nil")
	}
}
