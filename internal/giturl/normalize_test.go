package giturl

import "testing"

func TestNormalize(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"https://github.com/owner/repo.git":         "github.com/owner/repo",
		"https://github.com/owner/repo":             "github.com/owner/repo",
		"http://github.com/owner/repo/":             "github.com/owner/repo",
		"git@github.com:owner/repo.git":             "github.com/owner/repo",
		"ssh://git@github.com/owner/repo.git":       "github.com/owner/repo",
		"ssh://git@github.com:22/owner/repo.git":    "github.com/owner/repo",
		"https://user:token@github.com/owner/repo":  "github.com/owner/repo",
		"  https://GitHub.com/Owner/Repo.git  ":     "github.com/owner/repo",
		"git://github.com/owner/repo.git":           "github.com/owner/repo",
		"https://gitlab.example.com/group/sub/repo": "gitlab.example.com/group/sub/repo",
		"": "",
	}

	for input, want := range tests {
		if got := Normalize(input); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", input, got, want)
		}
	}
}
