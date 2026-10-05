package instances

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/truepace-io-oss/gitlab-mcp-server/internal/config"
	"github.com/truepace-io-oss/gitlab-mcp-server/internal/metrics"
)

// tokenTransport injects the GitLab personal access token. File-backed tokens are
// re-read on every request, so a rotated secret (projected file, external secret
// operator, …) is picked up without restarting the process.
type tokenTransport struct {
	next      http.RoundTripper
	token     string
	tokenFile string
}

func (t *tokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := valueOrFile(t.token, t.tokenFile)
	if err != nil {
		return nil, fmt.Errorf("gitlab token: %w", err)
	}
	// RoundTrippers must not modify the request they are given.
	r2 := req.Clone(req.Context())
	r2.Header.Set("PRIVATE-TOKEN", tok)
	return t.next.RoundTrip(r2)
}

// valueOrFile returns the inline value, or the trimmed contents of the file.
func valueOrFile(inline, file string) (string, error) {
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("read %q: %w", file, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return inline, nil
}

// buildHTTPClient assembles the HTTP client for one instance: a base transport
// with the configured TLS trust, wrapped with the token injector and the
// outbound-metrics RoundTripper, with the configured request timeout.
func buildHTTPClient(in config.Instance) (*http.Client, error) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("unexpected default transport type")
	}
	tr := base.Clone()

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if in.TLS.InsecureSkipTLSVerify {
		tlsCfg.InsecureSkipVerify = true
	} else {
		pool, err := loadCACertPool(in.TLS)
		if err != nil {
			return nil, fmt.Errorf("instance %q: %w", in.Name, err)
		}
		if pool != nil {
			tlsCfg.RootCAs = pool
		}
	}
	tr.TLSClientConfig = tlsCfg

	var rt http.RoundTripper = tr
	rt = &tokenTransport{next: rt, token: in.Token, tokenFile: in.TokenFile}
	rt = metrics.NewRoundTripper(rt, in.Name)

	timeout, err := time.ParseDuration(in.Timeout)
	if err != nil {
		return nil, fmt.Errorf("instance %q: invalid timeout: %w", in.Name, err)
	}
	return &http.Client{Transport: rt, Timeout: timeout}, nil
}

// loadCACertPool builds an x509 pool from the TLS config, or returns nil
// (meaning "use the system roots") when no explicit CA is configured.
func loadCACertPool(t config.TLS) (*x509.CertPool, error) {
	var pem []byte
	switch {
	case t.CAFile != "":
		b, err := os.ReadFile(t.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read caFile: %w", err)
		}
		pem = b
	case t.CAData != "":
		b, err := base64.StdEncoding.DecodeString(t.CAData)
		if err != nil {
			return nil, fmt.Errorf("caData is not valid base64: %w", err)
		}
		pem = b
	default:
		return nil, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no valid certificates found in CA")
	}
	return pool, nil
}
