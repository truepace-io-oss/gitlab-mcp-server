package gitlab

import (
	"fmt"
	"regexp"
	"strings"
)

// The deny list is layer 3 of the permission model and is NOT configurable. It
// names the API surfaces that carry credential material, or that are simply out
// of scope for this server. The HTTP client checks it immediately before every
// request, so no future tool — however it is wired — can reach them.
//
// Patterns match the path only, after the /api/v4 prefix has been stripped.
var (
	// allowExceptions win over denyPatterns and are checked first.
	allowExceptions = []*regexp.Regexp{
		// Reading the server's own token (name, scopes, expiry) is how
		// instances_list surfaces rotation warnings. It exposes no secret.
		regexp.MustCompile(`^/personal_access_tokens/self$`),
	}

	denyPatterns = []struct {
		re     *regexp.Regexp
		reason string
	}{
		// --- CI/CD variables: the pipeline secrets ---
		{regexp.MustCompile(`^/projects/[^/]+/variables`), "project CI/CD variables are pipeline secrets"},
		{regexp.MustCompile(`^/groups/[^/]+/variables`), "group CI/CD variables are pipeline secrets"},
		{regexp.MustCompile(`^/admin/ci/variables`), "instance CI/CD variables are pipeline secrets"},
		{regexp.MustCompile(`^/projects/[^/]+/pipelines/[^/]+/variables`), "pipeline variables are pipeline secrets"},

		// --- other secret stores ---
		{regexp.MustCompile(`^/projects/[^/]+/secure_files`), "secure files hold certificates and keystores"},
		{regexp.MustCompile(`^/projects/[^/]+/access_tokens`), "project access tokens are credential material"},
		{regexp.MustCompile(`^/groups/[^/]+/access_tokens`), "group access tokens are credential material"},
		{regexp.MustCompile(`^/personal_access_tokens`), "personal access tokens are credential material"},
		{regexp.MustCompile(`^/users/[^/]+/personal_access_tokens`), "personal access tokens are credential material"},
		{regexp.MustCompile(`^/projects/[^/]+/deploy_tokens`), "deploy tokens are long-lived repository credentials"},
		{regexp.MustCompile(`^/groups/[^/]+/deploy_tokens`), "deploy tokens are long-lived repository credentials"},
		{regexp.MustCompile(`^/deploy_tokens`), "deploy tokens are long-lived repository credentials"},
		{regexp.MustCompile(`^/projects/[^/]+/deploy_keys`), "deploy keys are long-lived repository credentials"},
		{regexp.MustCompile(`^/deploy_keys`), "deploy keys are long-lived repository credentials"},
		{regexp.MustCompile(`^/projects/[^/]+/hooks`), "webhooks carry URLs and secret tokens"},
		{regexp.MustCompile(`^/groups/[^/]+/hooks`), "webhooks carry URLs and secret tokens"},
		{regexp.MustCompile(`^/hooks`), "webhooks carry URLs and secret tokens"},
		{regexp.MustCompile(`^/projects/[^/]+/(integrations|services)`), "integrations store third-party credentials"},
		{regexp.MustCompile(`^/projects/[^/]+/cluster_agents/[^/]+/tokens`), "cluster agent tokens are credential material"},
		{regexp.MustCompile(`^/runners/[^/]+/reset_authentication_token`), "runner tokens are credential material"},
		{regexp.MustCompile(`^/projects/[^/]+/runners/reset_registration_token`), "runner registration tokens are credential material"},
		{regexp.MustCompile(`^/groups/[^/]+/runners/reset_registration_token`), "runner registration tokens are credential material"},

		// --- out of scope: job artifacts (this server does no scanning) ---
		{regexp.MustCompile(`^/projects/[^/]+/jobs/[^/]+/artifacts`), "job artifacts are out of scope for this server"},
		{regexp.MustCompile(`^/projects/[^/]+/jobs/artifacts`), "job artifacts are out of scope for this server"},
		{regexp.MustCompile(`^/projects/[^/]+/artifacts`), "job artifacts are out of scope for this server"},

		// --- out of scope: destructive, and never exposed by any tool ---
		{regexp.MustCompile(`^/projects/[^/]+/jobs/[^/]+/erase`), "erasing a job destroys its artifacts and log"},
	}
)

// deniedError is returned when a request is refused by the deny list.
type deniedError struct {
	method string
	path   string
	reason string
}

func (e *deniedError) Error() string {
	return fmt.Sprintf("refused: %s %s is permanently blocked by this server (%s)", e.method, e.path, e.reason)
}

// IsDenied reports whether err came from the deny list.
func IsDenied(err error) bool {
	_, ok := err.(*deniedError)
	return ok
}

// CheckDenyList returns a non-nil error when the path must never be requested.
// It is intentionally method-agnostic: reading a secret is as unacceptable as
// writing one.
func CheckDenyList(method, path string) error {
	p := normalizePath(path)
	for _, ex := range allowExceptions {
		if ex.MatchString(p) {
			return nil
		}
	}
	for _, d := range denyPatterns {
		if d.re.MatchString(p) {
			return &deniedError{method: method, path: p, reason: d.reason}
		}
	}
	return nil
}

// normalizePath strips any query string and the /api/v4 prefix, and guarantees a
// leading slash, so patterns can be written against the bare resource path.
func normalizePath(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	path = strings.TrimPrefix(path, "/api/v4")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	// Collapse a trailing slash so "/projects/1/variables/" is caught too.
	if len(path) > 1 {
		path = strings.TrimRight(path, "/")
	}
	return path
}
