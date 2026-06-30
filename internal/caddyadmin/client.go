package caddyadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func New(baseURL string) (*Client, error) {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		return nil, fmt.Errorf("caddy admin api is not configured")
	}
	if _, err := url.Parse(base); err != nil {
		return nil, fmt.Errorf("invalid caddy admin api url: %w", err)
	}
	return &Client{
		baseURL: strings.TrimRight(base, "/"),
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}, nil
}

func routeIDForDomain(fqdn string) string {
	safe := strings.NewReplacer(".", "-", "*", "wildcard").Replace(strings.ToLower(strings.TrimSpace(fqdn)))
	return "orcastra-route-" + safe
}

func (c *Client) UpsertDomainRoute(ctx context.Context, fqdn, upstream string) error {
	if strings.TrimSpace(fqdn) == "" || strings.TrimSpace(upstream) == "" {
		return fmt.Errorf("fqdn and upstream are required")
	}
	routes, err := c.getRoutes(ctx)
	if err != nil {
		return err
	}
	id := routeIDForDomain(fqdn)
	newRoute := map[string]any{
		"@id": id,
		"match": []map[string]any{
			{"host": []string{fqdn}},
		},
		"handle": []map[string]any{
			{
				"handler": "reverse_proxy",
				"upstreams": []map[string]string{
					{"dial": upstream},
				},
			},
		},
	}

	replaced := false
	for i, r := range routes {
		if rid, _ := r["@id"].(string); rid == id {
			routes[i] = newRoute
			replaced = true
			break
		}
	}
	if !replaced {
		routes = append(routes, newRoute)
	}
	return c.setRoutes(ctx, routes)
}

func (c *Client) DeleteDomainRoute(ctx context.Context, fqdn string) error {
	id := routeIDForDomain(fqdn)
	routes, err := c.getRoutes(ctx)
	if err != nil {
		return err
	}
	filtered := make([]map[string]any, 0, len(routes))
	for _, r := range routes {
		if rid, _ := r["@id"].(string); rid == id {
			continue
		}
		filtered = append(filtered, r)
	}
	return c.setRoutes(ctx, filtered)
}

func (c *Client) getRoutes(ctx context.Context) ([]map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/config/apps/http/servers/srv0/routes", nil)
	if err != nil {
		return nil, fmt.Errorf("build caddy get routes request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read caddy routes: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return []map[string]any{}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("read caddy routes failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var routes []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&routes); err != nil {
		return nil, fmt.Errorf("decode caddy routes: %w", err)
	}
	return routes, nil
}

func (c *Client) setRoutes(ctx context.Context, routes []map[string]any) error {
	cfg, err := c.getFullConfig(ctx)
	if err != nil {
		return err
	}
	apps := getOrCreateMap(cfg, "apps")
	httpApp := getOrCreateMap(apps, "http")
	servers := getOrCreateMap(httpApp, "servers")
	srv0 := getOrCreateMap(servers, "srv0")
	if _, ok := srv0["listen"]; !ok {
		srv0["listen"] = []string{":80"}
	}
	srv0["routes"] = routes
	return c.loadConfig(ctx, cfg)
}

func (c *Client) loadConfig(ctx context.Context, cfg map[string]any) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal caddy config: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/load", bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("build caddy load request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("load caddy config: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("load caddy config failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *Client) getFullConfig(ctx context.Context) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/config/", nil)
	if err != nil {
		return nil, fmt.Errorf("build caddy get config request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read caddy config: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return map[string]any{
			"admin": map[string]any{
				"listen": ":2019",
			},
		}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("read caddy config failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var cfg map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode caddy config: %w", err)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return cfg, nil
}

func getOrCreateMap(parent map[string]any, key string) map[string]any {
	if cur, ok := parent[key]; ok {
		if m, castOK := cur.(map[string]any); castOK {
			return m
		}
	}
	m := map[string]any{}
	parent[key] = m
	return m
}
