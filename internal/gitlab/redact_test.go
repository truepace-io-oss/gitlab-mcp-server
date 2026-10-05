package gitlab

import (
	"encoding/json"
	"strings"
	"testing"
)

// The fixtures below use deliberately fake, structurally-valid credentials.

func TestRedactValueMasksSecretKeys(t *testing.T) {
	// A pipeline-schedule response is the realistic case: an ordinary object with
	// a `variables` array hanging off it.
	raw := `{
	  "id": 13,
	  "description": "nightly",
	  "ref": "main",
	  "variables": [{"key": "DEPLOY_TOKEN", "value": "glpat-AAAABBBBCCCCDDDDEEEE"}],
	  "owner": {"username": "bot"},
	  "nested": {"api_token": "secret-value", "harmless": "keep me"}
	}`
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(RedactValue(v))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	for _, leak := range []string{"glpat-AAAABBBBCCCCDDDDEEEE", "secret-value"} {
		if strings.Contains(s, leak) {
			t.Fatalf("redacted output still contains %q: %s", leak, s)
		}
	}
	if !strings.Contains(s, "keep me") {
		t.Fatalf("redaction removed a harmless field: %s", s)
	}
	if !strings.Contains(s, "nightly") || !strings.Contains(s, "bot") {
		t.Fatalf("redaction removed useful context: %s", s)
	}
}

func TestRedactTextMasksCredentialShapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		leak string
	}{
		{"personal access token", "export TOKEN=glpat-AAAABBBBCCCCDDDDEEEE", "glpat-AAAABBBBCCCCDDDDEEEE"},
		{"runner token", "registered with glrt-ZZZZYYYYXXXXWWWWVVVV", "glrt-ZZZZYYYYXXXXWWWWVVVV"},
		{"deploy token", "gldt-1234567890abcdefghij ok", "gldt-1234567890abcdefghij"},
		{"jwt", "Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.c2lnbmF0dXJl", "eyJhbGciOiJSUzI1NiJ9"},
		{"aws key", "AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE", "AKIAIOSFODNN7EXAMPLE"},
		{"private key", "-----BEGIN RSA PRIVATE KEY-----", "BEGIN RSA PRIVATE KEY"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RedactText(c.in)
			if strings.Contains(got, c.leak) {
				t.Fatalf("RedactText left %q in %q", c.leak, got)
			}
			if !strings.Contains(got, redacted) {
				t.Fatalf("RedactText did not mark a redaction in %q", got)
			}
		})
	}
}

// A job log is mostly ordinary output; redaction must not destroy it.
func TestRedactTextKeepsOrdinaryLogLines(t *testing.T) {
	in := strings.Join([]string{
		"$ go test ./...",
		"ok  	example.com/pkg	0.012s",
		"Job succeeded",
	}, "\n")
	if got := RedactText(in); got != in {
		t.Fatalf("ordinary log was altered:\n%s", got)
	}
}

func TestRedactSecret(t *testing.T) {
	if got := RedactSecret("glpat-AAAABBBBCCCCDDDDEEEE"); got != "glpa…EEEE" {
		t.Fatalf("RedactSecret = %q", got)
	}
	if got := RedactSecret("short"); got != redacted {
		t.Fatalf("short secret must be fully redacted, got %q", got)
	}
}

func TestParseErrorBodyShapes(t *testing.T) {
	cases := map[string]string{
		`{"message":"404 Project Not Found"}`:             "404 Project Not Found",
		`{"error":"insufficient_scope"}`:                  "insufficient_scope",
		`{"message":{"name":["has already been taken"]}}`: "name has already been taken",
		`{"message":{"base":["a"],"path":["b"]}}`:         "base a; path b",
	}
	for in, want := range cases {
		if got := parseErrorBody([]byte(in)); got != want {
			t.Errorf("parseErrorBody(%s) = %q, want %q", in, got, want)
		}
	}
	// Non-JSON bodies must still surface something usable.
	if got := parseErrorBody([]byte("<html>502 Bad Gateway</html>")); !strings.Contains(got, "502") {
		t.Errorf("non-JSON body lost its content: %q", got)
	}
	// And an error body that itself contains a token must be redacted.
	if got := parseErrorBody([]byte(`{"message":"bad token glpat-AAAABBBBCCCCDDDDEEEE"}`)); strings.Contains(got, "glpat-AAAABBBBCCCCDDDDEEEE") {
		t.Errorf("error body leaked a token: %q", got)
	}
}
