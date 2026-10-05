package gitlab

import (
	"regexp"
	"strings"
)

// Redaction is the last line of defence before anything reaches the model. Even
// though the deny list keeps the obvious secret endpoints unreachable, ordinary
// responses can embed credentials — a pipeline schedule carries a `variables`
// array, and a job log can contain a token an author forgot to mask.

// secretKeyRe matches JSON object keys whose values must never be shown.
var secretKeyRe = regexp.MustCompile(`(?i)(variable|secret|token|password|passphrase|private_key|credential|authorization|x-api-key)`)

// credentialShapes matches credential-looking substrings inside free text.
var credentialShapes = []*regexp.Regexp{
	regexp.MustCompile(`glpat-[A-Za-z0-9_\-]{8,}`),                                        // personal access token
	regexp.MustCompile(`glptt-[A-Za-z0-9_\-]{8,}`),                                        // trigger token
	regexp.MustCompile(`gldt-[A-Za-z0-9_\-]{8,}`),                                         // deploy token
	regexp.MustCompile(`glrt-[A-Za-z0-9_\-]{8,}`),                                         // runner token
	regexp.MustCompile(`gloas-[A-Za-z0-9_\-]{8,}`),                                        // OAuth application secret
	regexp.MustCompile(`glcbt-[A-Za-z0-9_\-]{8,}`),                                        // CI build token
	regexp.MustCompile(`glimt-[A-Za-z0-9_\-]{8,}`),                                        // incoming mail token
	regexp.MustCompile(`glsoat-[A-Za-z0-9_\-]{8,}`),                                       // SCIM OAuth token
	regexp.MustCompile(`eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{5,}`), // JWT
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), // AWS access key id
}

const redacted = "<redacted>"

// RedactValue walks a decoded JSON value and replaces anything that looks like a
// secret. Maps are rebuilt rather than mutated in place so a caller's value is
// never altered behind its back.
func RedactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if secretKeyRe.MatchString(k) {
				out[k] = redacted
				continue
			}
			out[k] = RedactValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = RedactValue(val)
		}
		return out
	case string:
		return RedactText(t)
	default:
		return v
	}
}

// RedactText replaces credential-shaped substrings in free text. It is applied to
// job logs and to any YAML or message rendered for the model.
func RedactText(s string) string {
	for _, re := range credentialShapes {
		s = re.ReplaceAllString(s, redacted)
	}
	return s
}

// RedactSecret shortens a known secret to a non-reversible hint. It is used when
// a value must be referenced at all (never for the value itself).
func RedactSecret(s string) string {
	if len(s) <= 8 {
		return redacted
	}
	var b strings.Builder
	b.WriteString(s[:4])
	b.WriteString("…")
	b.WriteString(s[len(s)-4:])
	return b.String()
}
