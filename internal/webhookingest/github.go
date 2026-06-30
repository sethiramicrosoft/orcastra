package webhookingest

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

type GitHubPushEvent struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	HeadCommit struct {
		Message string `json:"message"`
	} `json:"head_commit"`
	Repository struct {
		FullName string `json:"full_name"` // owner/repo
		HTMLURL  string `json:"html_url"`
		CloneURL string `json:"clone_url"`
		SSHURL   string `json:"ssh_url"`
	} `json:"repository"`
}

func ParseGitHubPushEvent(body []byte) (*GitHubPushEvent, error) {
	var e GitHubPushEvent
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, fmt.Errorf("parse github push payload: %w", err)
	}
	if e.Ref == "" || e.Repository.FullName == "" {
		return nil, fmt.Errorf("invalid github push payload: missing ref or repository full_name")
	}
	return &e, nil
}

func VerifyGitHubSignature(secret string, body []byte, signatureHeader string) bool {
	if secret == "" {
		return false
	}
	if !strings.HasPrefix(signatureHeader, "sha256=") {
		return false
	}
	signatureHex := strings.TrimPrefix(signatureHeader, "sha256=")
	given, err := hex.DecodeString(signatureHex)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)
	return hmac.Equal(given, expected)
}

func BranchFromRef(ref string) string {
	const prefix = "refs/heads/"
	if strings.HasPrefix(ref, prefix) {
		return strings.TrimPrefix(ref, prefix)
	}
	return ref
}

func NormalizeRepo(input string) string {
	s := strings.TrimSpace(strings.ToLower(input))
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimPrefix(s, "https://github.com/")
	s = strings.TrimPrefix(s, "http://github.com/")
	s = strings.TrimPrefix(s, "git@github.com:")
	return strings.Trim(s, "/")
}
