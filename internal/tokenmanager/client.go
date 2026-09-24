// Package tokenmanager validates API tokens through the external
// Token-Manager service.
package tokenmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout       = 2 * time.Second
	maxResponseBodyBytes = 64 << 10
)

var (
	// ErrInvalidToken means Token-Manager authenticated the caller and
	// reported that the presented token is not valid for this app.
	ErrInvalidToken = errors.New("invalid API token")
	// ErrInvalidClient means the configured Token-Manager client credentials
	// were rejected. This is an operator configuration problem, not a bad
	// bearer token presented by the caller.
	ErrInvalidClient = errors.New("invalid Token-Manager client credentials")
	// ErrUnavailable means Token-Manager could not provide a trustworthy
	// answer. Callers must fail closed.
	ErrUnavailable = errors.New("Token-Manager unavailable")
)

// TokenInfo is the identity returned by a successful validation.
type TokenInfo struct {
	ID        string
	Name      string
	App       string
	ExpiresAt string
}

// Client is a small, stateless client for the Token-Manager validation API.
type Client struct {
	baseURL      string
	clientID     string
	clientSecret string
	httpClient   *http.Client
}

// New creates a client for a Token-Manager base URL, for example
// http://127.0.0.1:8000. The base URL must not include /api/v1; the client
// appends the public validation path itself.
func New(baseURL, clientID, clientSecret string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid Token-Manager URL %q", baseURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("Token-Manager URL must use http or https")
	}
	if u.User != nil {
		return nil, fmt.Errorf("Token-Manager URL must not contain user credentials")
	}
	if strings.TrimSpace(clientID) == "" || strings.TrimSpace(clientSecret) == "" {
		return nil, errors.New("Token-Manager client ID and secret are required")
	}
	return &Client{
		baseURL:      baseURL,
		clientID:     strings.TrimSpace(clientID),
		clientSecret: strings.TrimSpace(clientSecret),
		httpClient:   &http.Client{Timeout: defaultTimeout},
	}, nil
}

// Validate asks Token-Manager whether raw is valid for this client app.
// It deliberately returns no token secret or manager response body to the
// caller. All errors other than ErrInvalidToken should be treated as an
// unavailable authentication service by the HTTP layer.
func (c *Client) Validate(ctx context.Context, raw string) (TokenInfo, error) {
	if c == nil {
		return TokenInfo{}, ErrUnavailable
	}
	if strings.TrimSpace(raw) == "" {
		return TokenInfo{}, ErrInvalidToken
	}

	body, err := json.Marshal(struct {
		Token string `json:"token"`
	}{Token: raw})
	if err != nil {
		return TokenInfo{}, fmt.Errorf("encode validation request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/validate", bytes.NewReader(body))
	if err != nil {
		return TokenInfo{}, fmt.Errorf("create validation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Client-Id", c.clientID)
	req.Header.Set("X-Client-Secret", c.clientSecret)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return TokenInfo{}, fmt.Errorf("%w: request failed", ErrUnavailable)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBodyBytes))

	if resp.StatusCode == http.StatusUnauthorized {
		if strings.EqualFold(strings.TrimSpace(resp.Header.Get("X-Error")), "invalid_client") {
			return TokenInfo{}, ErrInvalidClient
		}
		return TokenInfo{}, ErrInvalidToken
	}
	if resp.StatusCode != http.StatusNoContent {
		return TokenInfo{}, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}

	info := TokenInfo{
		ID:        strings.TrimSpace(resp.Header.Get("X-Token-Id")),
		Name:      strings.TrimSpace(resp.Header.Get("X-Token-Name")),
		App:       strings.TrimSpace(resp.Header.Get("X-Token-App")),
		ExpiresAt: strings.TrimSpace(resp.Header.Get("X-Token-Expires-At")),
	}
	if info.ID == "" || info.Name == "" || info.App == "" || info.ExpiresAt == "" {
		return TokenInfo{}, fmt.Errorf("%w: validation response missing metadata", ErrUnavailable)
	}
	return info, nil
}

// Health checks the Token-Manager health endpoint without using client
// credentials. It is intended for diagnostics, not authorization.
func (c *Client) Health(ctx context.Context) error {
	if c == nil {
		return ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/health", nil)
	if err != nil {
		return fmt.Errorf("create health request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: health request failed", ErrUnavailable)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBodyBytes))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: health status %d", ErrUnavailable, resp.StatusCode)
	}
	return nil
}
