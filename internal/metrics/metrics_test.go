package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordTool(t *testing.T) {
	RecordTool("projects_list", "gl", "ok", 10*time.Millisecond)
	if got := testutil.ToFloat64(toolCalls.WithLabelValues("projects_list", "gl", "ok")); got != 1 {
		t.Fatalf("tool call counter = %v, want 1", got)
	}
}

// An empty instance must become "-" rather than an empty label value, so the
// series stays readable.
func TestRecordToolNormalisesEmptyInstance(t *testing.T) {
	RecordTool("instances_list", "", "ok", time.Millisecond)
	if got := testutil.ToFloat64(toolCalls.WithLabelValues("instances_list", "-", "ok")); got != 1 {
		t.Fatalf("expected the empty instance to be recorded as \"-\", got %v", got)
	}
}

func TestRecordAuthAndBlocked(t *testing.T) {
	RecordAuth("static", "allow")
	if got := testutil.ToFloat64(authRequests.WithLabelValues("static", "allow")); got != 1 {
		t.Fatalf("auth counter = %v", got)
	}
	RecordWriteBlocked("gl", "capability_denied")
	if got := testutil.ToFloat64(writesBlocked.WithLabelValues("gl", "capability_denied")); got != 1 {
		t.Fatalf("blocked counter = %v", got)
	}
}

func TestRecordOperation(t *testing.T) {
	RecordOperation("gl", "pipeline_retry", "ok")
	if got := testutil.ToFloat64(operations.WithLabelValues("gl", "pipeline_retry", "ok")); got != 1 {
		t.Fatalf("operations counter = %v", got)
	}
}

func TestSetInstanceUp(t *testing.T) {
	SetInstanceUp("gl", true)
	if got := testutil.ToFloat64(instanceUp.WithLabelValues("gl")); got != 1 {
		t.Fatalf("instance_up = %v, want 1", got)
	}
	SetInstanceUp("gl", false)
	if got := testutil.ToFloat64(instanceUp.WithLabelValues("gl")); got != 0 {
		t.Fatalf("instance_up = %v, want 0", got)
	}
}

// A token with no expiry must have its gauge removed, so "never expires" is not
// reported as "expires now" — which would page someone at 3am for nothing.
func TestTokenExpiryGaugeIsClearable(t *testing.T) {
	SetTokenExpiresIn("gl", 3600)
	if got := testutil.ToFloat64(tokenExpiresIn.WithLabelValues("gl")); got != 3600 {
		t.Fatalf("token gauge = %v", got)
	}
	ClearTokenExpiresIn("gl")
	if n := testutil.CollectAndCount(tokenExpiresIn); n != 0 {
		t.Fatalf("expected the gauge to be removed, %d series remain", n)
	}
}

func TestSetBuildInfo(t *testing.T) {
	SetBuildInfo("1.2.3")
	if n := testutil.CollectAndCount(buildInfo); n == 0 {
		t.Fatal("build info was not published")
	}
}

// The RoundTripper records the method and status code, and picks the remaining
// API budget out of the response headers.
func TestRoundTripperRecordsAndReadsRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("RateLimit-Remaining", "1234")
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{Transport: NewRoundTripper(http.DefaultTransport, "rt-test")}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if got := testutil.ToFloat64(gitlabRequests.WithLabelValues("rt-test", http.MethodGet, "418")); got != 1 {
		t.Fatalf("request counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(rateLimitRemaining.WithLabelValues("rt-test")); got != 1234 {
		t.Fatalf("rate limit gauge = %v, want 1234", got)
	}
}

// A transport error must still be counted, under a bounded label.
func TestRoundTripperCountsTransportErrors(t *testing.T) {
	client := &http.Client{Transport: NewRoundTripper(http.DefaultTransport, "rt-err")}
	// Port 1 on loopback refuses connections.
	if _, err := client.Get("http://127.0.0.1:1/"); err == nil {
		t.Fatal("expected a transport error")
	}
	if got := testutil.ToFloat64(gitlabRequests.WithLabelValues("rt-err", http.MethodGet, "error")); got != 1 {
		t.Fatalf("error counter = %v, want 1", got)
	}
}
