// Package gitlab is the typed client for the GitLab REST API v4 used by this
// server. Every call takes the capability it needs as its first argument, so the
// mapping from tool to permission is visible at the call site and enforced
// before a request leaves the process.
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/truepace-io-oss/gitlab-mcp-server/internal/metrics"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/perm"
)

// maxRetryAfter caps how long a 429 retry will wait before giving up and telling
// the model to come back later.
const maxRetryAfter = 30 * time.Second

// maxErrorBody bounds how much of an error response is read.
const maxErrorBody = 64 << 10

// Client talks to one GitLab instance.
type Client struct {
	// Name is the instance name, used as the metric label.
	Name string
	// BaseURL is the API root, e.g. https://gitlab.com/api/v4
	BaseURL string
	HTTP    *http.Client
	Guard   *perm.Guard
	Page    PageOpts
	// MinRemaining is the rate-limit floor below which non-essential list calls
	// are refused.
	MinRemaining int

	// remaining tracks the last seen RateLimit-Remaining value; -1 = unknown.
	remaining int
}

// PageOpts bounds list traversal.
type PageOpts struct {
	PerPage  int
	MaxPages int
}

// New builds a Client. It does not contact the server.
func New(name, baseURL string, httpClient *http.Client, guard *perm.Guard, page PageOpts, minRemaining int) *Client {
	return &Client{
		Name:         name,
		BaseURL:      strings.TrimRight(baseURL, "/") + "/api/v4",
		HTTP:         httpClient,
		Guard:        guard,
		Page:         page,
		MinRemaining: minRemaining,
		remaining:    -1,
	}
}

// Response carries the parts of an HTTP response the callers need.
type Response struct {
	StatusCode int
	Header     http.Header
	NextPage   string
}

// Do performs one request. The order of operations is deliberate and must not be
// rearranged: capability, then deny list, then the request itself.
func (c *Client) Do(ctx context.Context, cap perm.Capability, method, path string, query url.Values, body any, out any) (*Response, error) {
	if err := c.Guard.Allow(cap); err != nil {
		return nil, err
	}
	if err := CheckDenyList(method, path); err != nil {
		metrics.RecordWriteBlocked(c.Name, perm.ReasonDenyListed)
		return nil, err
	}
	return c.do(ctx, method, path, query, body, out)
}

// DoList is Do for list endpoints. It additionally respects the rate-limit floor,
// so a chatty agent cannot exhaust the budget that single reads and pipeline
// operations need.
func (c *Client) DoList(ctx context.Context, cap perm.Capability, path string, query url.Values, out any) (*Response, error) {
	if err := c.Guard.Allow(cap); err != nil {
		return nil, err
	}
	if err := CheckDenyList(http.MethodGet, path); err != nil {
		metrics.RecordWriteBlocked(c.Name, perm.ReasonDenyListed)
		return nil, err
	}
	if c.remaining >= 0 && c.remaining < c.MinRemaining {
		metrics.RecordWriteBlocked(c.Name, perm.ReasonRateLimited)
		return nil, fmt.Errorf("refusing a list request: only %d GitLab API requests remain before the rate limit (floor is %d). "+
			"Narrow the query, or wait for the window to reset", c.remaining, c.MinRemaining)
	}
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) (*Response, error) {
	resp, err := c.send(ctx, method, path, query, body, out)
	if err == nil {
		return resp, nil
	}
	// A single retry on 429, then hand the problem to the model.
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Status != http.StatusTooManyRequests {
		return resp, err
	}
	wait := time.Duration(apiErr.RetryAfter) * time.Second
	if wait <= 0 || wait > maxRetryAfter {
		return resp, err
	}
	select {
	case <-ctx.Done():
		return resp, ctx.Err()
	case <-time.After(wait):
	}
	return c.send(ctx, method, path, query, body, out)
}

func (c *Client) send(ctx context.Context, method, path string, query url.Values, body, out any) (*Response, error) {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	httpResp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitLab API %s %s: %w", method, path, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, httpResp.Body)
		_ = httpResp.Body.Close()
	}()

	if v := httpResp.Header.Get("RateLimit-Remaining"); v != "" {
		if n, perr := strconv.Atoi(v); perr == nil {
			c.remaining = n
		}
	}

	res := &Response{
		StatusCode: httpResp.StatusCode,
		Header:     httpResp.Header,
		NextPage:   httpResp.Header.Get("X-Next-Page"),
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(httpResp.Body, maxErrorBody))
		apiErr := &APIError{
			Status:  httpResp.StatusCode,
			Message: parseErrorBody(raw),
			Method:  method,
			Path:    path,
		}
		if ra := httpResp.Header.Get("Retry-After"); ra != "" {
			if n, perr := strconv.Atoi(ra); perr == nil {
				apiErr.RetryAfter = n
			}
		}
		return res, apiErr
	}

	if out != nil {
		raw, rerr := io.ReadAll(httpResp.Body)
		if rerr != nil {
			return res, fmt.Errorf("read response body: %w", rerr)
		}
		if len(raw) > 0 {
			if err := decodeRedacted(raw, out); err != nil {
				return res, fmt.Errorf("decode response from %s %s: %w", method, path, err)
			}
		}
	}
	return res, nil
}

// decodeRedacted routes the payload through the redactor before it reaches the
// typed struct, so a secret-bearing field can never survive into a rendered
// tool result even if a struct gains such a field later.
func decodeRedacted(raw []byte, out any) error {
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return err
	}
	clean, err := json.Marshal(RedactValue(generic))
	if err != nil {
		return err
	}
	return json.Unmarshal(clean, out)
}

// RawText performs a GET whose response is plain text rather than JSON (the job
// trace endpoint). The result is passed through RedactText.
func (c *Client) RawText(ctx context.Context, cap perm.Capability, path string, query url.Values, limit int64) (string, error) {
	if err := c.Guard.Allow(cap); err != nil {
		return "", err
	}
	if err := CheckDenyList(http.MethodGet, path); err != nil {
		metrics.RecordWriteBlocked(c.Name, perm.ReasonDenyListed)
		return "", err
	}

	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("GitLab API GET %s: %w", path, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return "", &APIError{Status: resp.StatusCode, Message: parseErrorBody(raw), Method: http.MethodGet, Path: path}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return "", fmt.Errorf("read response body: %w", err)
	}
	return RedactText(string(raw)), nil
}

// esc encodes a project or group reference for use as a path segment. GitLab
// expects a URL-encoded full path ("group%2Fproject") or a numeric id.
func esc(ref string) string {
	return url.PathEscape(strings.Trim(ref, "/"))
}

// Esc is the exported form of esc, for callers assembling paths.
func Esc(ref string) string { return esc(ref) }
