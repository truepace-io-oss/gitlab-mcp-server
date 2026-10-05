package gitlab

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// APIError is a non-2xx response from GitLab. The message is flattened from
// whichever shape GitLab used, so the model sees GitLab's own words.
type APIError struct {
	Status  int
	Message string
	Method  string
	Path    string
	// RetryAfter is set from the Retry-After header on 429 responses.
	RetryAfter int
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("GitLab API %s %s returned %d", e.Method, e.Path, e.Status)
	}
	return fmt.Sprintf("GitLab API %s %s returned %d: %s", e.Method, e.Path, e.Status, e.Message)
}

// IsForbidden reports a 401/403 — the token lacks the permission.
func IsForbidden(err error) bool {
	a, ok := err.(*APIError)
	return ok && (a.Status == http.StatusForbidden || a.Status == http.StatusUnauthorized)
}

// IsNotFound reports a 404. GitLab also returns 404 instead of 403 for resources
// outside a token's boundary, which is why the tool layer keeps both messages.
func IsNotFound(err error) bool {
	a, ok := err.(*APIError)
	return ok && a.Status == http.StatusNotFound
}

// IsRateLimited reports a 429.
func IsRateLimited(err error) bool {
	a, ok := err.(*APIError)
	return ok && a.Status == http.StatusTooManyRequests
}

// IsConflict reports a 409.
func IsConflict(err error) bool {
	a, ok := err.(*APIError)
	return ok && a.Status == http.StatusConflict
}

// parseErrorBody flattens GitLab's several error shapes into one line:
//
//	{"message": "404 Project Not Found"}
//	{"error": "insufficient_scope"}
//	{"message": {"name": ["has already been taken"]}}
//	{"message": {"base": ["..."], "path": ["..."]}}
func parseErrorBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		// Not JSON: return a trimmed snippet so the cause is still visible.
		s := strings.TrimSpace(string(body))
		if len(s) > 200 {
			s = s[:200] + "…"
		}
		return RedactText(s)
	}
	for _, key := range []string{"message", "error", "error_description"} {
		v, ok := raw[key]
		if !ok {
			continue
		}
		if s := flatten(v); s != "" {
			return RedactText(s)
		}
	}
	return ""
}

// flatten renders a string, a list of strings, or a field->messages map as one
// readable line with deterministic ordering.
func flatten(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			if s := flatten(e); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "; ")
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			if s := flatten(t[k]); s != "" {
				parts = append(parts, fmt.Sprintf("%s %s", k, s))
			}
		}
		return strings.Join(parts, "; ")
	default:
		return ""
	}
}
