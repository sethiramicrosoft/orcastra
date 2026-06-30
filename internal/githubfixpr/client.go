package githubfixpr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	token string
	http  *http.Client
}

func New(token string) (*Client, error) {
	t := strings.TrimSpace(token)
	if t == "" {
		return nil, fmt.Errorf("github token is not configured")
	}
	return &Client{
		token: t,
		http: &http.Client{
			Timeout: 20 * time.Second,
		},
	}, nil
}

type CreateFixPRInput struct {
	Repo        string
	Deployment  string
	CommitSHA   string
	Diagnosis   string
	Suggestion  string
	ServiceName string
}

type CreateFixPROutput struct {
	URL        string
	Number     int
	Branch     string
	BaseBranch string
	Repo       string
}

func (c *Client) CreateFixPR(ctx context.Context, in CreateFixPRInput) (*CreateFixPROutput, error) {
	repo := strings.Trim(strings.ToLower(in.Repo), "/")
	if repo == "" || !strings.Contains(repo, "/") {
		return nil, fmt.Errorf("invalid repo")
	}
	defaultBranch, headSHA, err := c.fetchDefaultBranchAndSHA(ctx, repo)
	if err != nil {
		return nil, err
	}
	branch := "orcastra/fix-" + short(in.Deployment)
	if err := c.createBranchIfMissing(ctx, repo, branch, headSHA); err != nil {
		return nil, err
	}

	filePath := fmt.Sprintf(".orcastra/fixes/%s.md", in.Deployment)
	content := c.renderFixDocument(in)
	if err := c.putFile(ctx, repo, branch, filePath, content, fmt.Sprintf("orcastra: add fix plan for deployment %s", in.Deployment)); err != nil {
		return nil, err
	}

	title := fmt.Sprintf("fix: resolve failed deploy %s", short(in.Deployment))
	body := c.renderPRBody(in, filePath)
	url, number, err := c.createDraftPR(ctx, repo, title, body, branch, defaultBranch)
	if err != nil {
		return nil, err
	}
	return &CreateFixPROutput{
		URL:        url,
		Number:     number,
		Branch:     branch,
		BaseBranch: defaultBranch,
		Repo:       repo,
	}, nil
}

func (c *Client) fetchDefaultBranchAndSHA(ctx context.Context, repo string) (string, string, error) {
	var repoResp struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.api(ctx, http.MethodGet, "https://api.github.com/repos/"+repo, nil, &repoResp); err != nil {
		return "", "", fmt.Errorf("read repo metadata: %w", err)
	}
	if strings.TrimSpace(repoResp.DefaultBranch) == "" {
		return "", "", fmt.Errorf("default branch not found")
	}
	var refResp struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.api(ctx, http.MethodGet, "https://api.github.com/repos/"+repo+"/git/ref/heads/"+repoResp.DefaultBranch, nil, &refResp); err != nil {
		return "", "", fmt.Errorf("read default branch ref: %w", err)
	}
	if strings.TrimSpace(refResp.Object.SHA) == "" {
		return "", "", fmt.Errorf("default branch sha not found")
	}
	return repoResp.DefaultBranch, refResp.Object.SHA, nil
}

func (c *Client) createBranchIfMissing(ctx context.Context, repo, branch, sha string) error {
	body := map[string]string{
		"ref": "refs/heads/" + branch,
		"sha": sha,
	}
	err := c.api(ctx, http.MethodPost, "https://api.github.com/repos/"+repo+"/git/refs", body, nil)
	if err == nil {
		return nil
	}
	// Branch may already exist.
	if strings.Contains(err.Error(), "Reference already exists") {
		return nil
	}
	return fmt.Errorf("create branch: %w", err)
}

func (c *Client) putFile(ctx context.Context, repo, branch, path, content, message string) error {
	body := map[string]string{
		"message": message,
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"branch":  branch,
	}
	return c.api(ctx, http.MethodPut, "https://api.github.com/repos/"+repo+"/contents/"+path, body, nil)
}

func (c *Client) createDraftPR(ctx context.Context, repo, title, body, head, base string) (string, int, error) {
	reqBody := map[string]any{
		"title": title,
		"body":  body,
		"head":  head,
		"base":  base,
		"draft": true,
	}
	var resp struct {
		HTMLURL string `json:"html_url"`
		Number  int    `json:"number"`
	}
	if err := c.api(ctx, http.MethodPost, "https://api.github.com/repos/"+repo+"/pulls", reqBody, &resp); err != nil {
		return "", 0, fmt.Errorf("create pull request: %w", err)
	}
	return resp.HTMLURL, resp.Number, nil
}

func (c *Client) api(ctx context.Context, method, url string, reqBody any, out any) error {
	var body io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal github request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return fmt.Errorf("build github request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("github api status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		if decErr := json.NewDecoder(resp.Body).Decode(out); decErr != nil && decErr != io.EOF {
			return fmt.Errorf("decode github response: %w", decErr)
		}
	}
	return nil
}

func (c *Client) renderFixDocument(in CreateFixPRInput) string {
	return fmt.Sprintf(`# Orcastra deployment fix plan

- Deployment: %s
- Service: %s
- Commit SHA: %s

## Diagnosis
%s

## Suggested fix
%s
`, in.Deployment, in.ServiceName, in.CommitSHA, strings.TrimSpace(in.Diagnosis), strings.TrimSpace(in.Suggestion))
}

func (c *Client) renderPRBody(in CreateFixPRInput, filePath string) string {
	return fmt.Sprintf(`This draft PR was created by Orcastra from a failed deployment.

Deployment: %s
Service: %s
Commit: %s

Diagnosis:
%s

Suggested fix:
%s

A generated fix plan was added at:
- %s
`, in.Deployment, in.ServiceName, in.CommitSHA, strings.TrimSpace(in.Diagnosis), strings.TrimSpace(in.Suggestion), filePath)
}

func short(v string) string {
	if len(v) <= 8 {
		return v
	}
	return v[:8]
}
