// Package giturl provides canonicalisation for repository URLs.
//
// GitHub reports the same repository in several forms (clone_url, ssh_url,
// html_url), and users may configure any of them. Normalising every URL to a
// single "host/owner/repo" form lets us match a webhook to a project without
// relying on brittle string comparison.
package giturl

import "strings"

// Normalize converts common Git URL forms into "host/owner/repo".
//
// Examples:
//
//	https://github.com/owner/repo.git      -> github.com/owner/repo
//	git@github.com:owner/repo.git          -> github.com/owner/repo
//	ssh://git@github.com/owner/repo        -> github.com/owner/repo
//	https://user:token@github.com/owner/repo -> github.com/owner/repo
func Normalize(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	hasScheme := strings.Contains(s, "://")

	// Convert scp-like syntax (git@host:owner/repo) to a slash-separated path.
	// Scheme URLs are skipped so that ssh://host:port/... keeps its port.
	if !hasScheme {
		if at := strings.Index(s, "@"); at >= 0 {
			if colon := strings.Index(s[at:], ":"); colon >= 0 {
				between := s[at+1 : at+colon]
				if !strings.Contains(between, "/") {
					s = s[:at+1] + between + "/" + s[at+colon+1:]
				}
			}
		}
	}

	for _, prefix := range []string{"https://", "http://", "ssh://", "git://"} {
		s = strings.TrimPrefix(s, prefix)
	}

	// Strip userinfo, including credentials.
	if at := strings.Index(s, "@"); at >= 0 {
		s = s[at+1:]
	}

	// Drop an explicit port from the host (e.g. host:22/owner/repo).
	if slash := strings.Index(s, "/"); slash > 0 {
		host := s[:slash]
		if colon := strings.Index(host, ":"); colon > 0 && isDigits(host[colon+1:]) {
			s = host[:colon] + s[slash:]
		}
	}

	return strings.TrimSuffix(s, "/")
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
