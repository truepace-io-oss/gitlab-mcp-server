package metrics

import (
	"net/http"
	"strconv"
	"time"
)

// roundTripper instruments outbound GitLab API calls. It deliberately records
// only the HTTP method and status code — never the path, which is unbounded.
type roundTripper struct {
	next     http.RoundTripper
	instance string
}

// NewRoundTripper wraps next with outbound request metrics for one instance.
func NewRoundTripper(next http.RoundTripper, instance string) http.RoundTripper {
	return &roundTripper{next: next, instance: instance}
}

func (t *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.next.RoundTrip(req)
	d := time.Since(start)

	code := "error"
	if resp != nil {
		code = strconv.Itoa(resp.StatusCode)
		if v := resp.Header.Get("RateLimit-Remaining"); v != "" {
			if n, perr := strconv.ParseFloat(v, 64); perr == nil {
				SetRateLimitRemaining(t.instance, n)
			}
		}
	}
	RecordGitLabRequest(t.instance, req.Method, code, d)
	return resp, err
}
