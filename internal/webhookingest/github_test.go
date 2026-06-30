package webhookingest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifyGitHubSignature(t *testing.T) {
	secret := "my-webhook-secret"
	body := []byte(`{"ref":"refs/heads/main","after":"abc123"}`)

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !VerifyGitHubSignature(secret, body, good) {
		t.Fatal("expected valid signature to pass")
	}

	bad := "sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if VerifyGitHubSignature(secret, body, bad) {
		t.Fatal("expected invalid signature to fail")
	}
}

func TestBranchFromRef(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "refs/heads/main", want: "main"},
		{in: "refs/heads/feature/auth", want: "feature/auth"},
		{in: "main", want: "main"},
	}
	for _, tc := range cases {
		got := BranchFromRef(tc.in)
		if got != tc.want {
			t.Fatalf("BranchFromRef(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeRepo(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "https://github.com/OWNER/Repo.git", want: "owner/repo"},
		{in: "http://github.com/OWNER/Repo", want: "owner/repo"},
		{in: "git@github.com:OWNER/Repo.git", want: "owner/repo"},
		{in: " owner/repo ", want: "owner/repo"},
	}
	for _, tc := range cases {
		got := NormalizeRepo(tc.in)
		if got != tc.want {
			t.Fatalf("NormalizeRepo(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
