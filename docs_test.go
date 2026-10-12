package main

import (
	"os"
	"strings"
	"testing"
)

// githubTokenPermissions is the read-only permission set of a fine-grained
// GitHub token that every read of a default scan needs, by GitHub's table:
// https://docs.github.com/en/rest/authentication/permissions-required-for-fine-grained-personal-access-tokens
// Contents is the one the table does not name: without it GitHub refused the
// repository listing's default branch of every private repository, measured.
var githubTokenPermissions = []string{"Actions", "Contents", "Commit statuses", "Pull requests", "Issues", "Code scanning alerts"}

func TestDocs_every_github_token_instruction_names_every_permission_a_scan_needs(t *testing.T) {
	for _, path := range []string{"README.md", "docs/configuration.md", "config.example.yaml"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		text := strings.Join(strings.Fields(strings.NewReplacer("#", " ", "*", "").Replace(string(raw))), " ")
		_, rest, found := strings.Cut(text, "fine-grained token with")
		sentence, _, _ := strings.Cut(rest, ".")
		if !found {
			t.Errorf("%s names no fine-grained GitHub token, want its permissions listed", path)
			continue
		}
		for _, p := range githubTokenPermissions {
			if !strings.Contains(sentence, p) {
				t.Errorf("%s fine-grained token permissions %q omit %q", path, sentence, p)
			}
		}
	}
}

// checksTradeoff is what every GitHub token instruction says about pull
// request checks: no fine-grained permission reads them, what the dashboard
// shows instead, the token that does read them, and where the choice is
// weighed.
var checksTradeoff = []string{"no Checks permission", "not available with this token", "classic token with the `repo` scope", "choosing-a-github-token"}

func TestDocs_every_github_token_instruction_names_the_checks_tradeoff(t *testing.T) {
	for _, path := range []string{"README.md", "docs/configuration.md", "config.example.yaml"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		text := strings.ToLower(strings.Join(strings.Fields(strings.NewReplacer("#", " ", "*", "").Replace(string(raw))), " "))
		for _, want := range checksTradeoff {
			if !strings.Contains(text, strings.ToLower(want)) {
				t.Errorf("%s does not say %q about a GitHub token and pull request checks", path, want)
			}
		}
	}
}

// ownerLimit is what every GitHub token instruction says about the reach of a
// fine-grained token: GitHub limits it to the private repositories and the
// code-scanning alerts of one resource owner, and a classic token is the way
// to read several owners'.
var ownerLimit = []string{
	"one resource owner",
	"private repositories under several owners",
	"code-scanning alerts only on its resource owner's repositories",
	"code scanning on another owner's public repositories",
}

func TestDocs_every_github_token_instruction_names_the_one_owner_limit(t *testing.T) {
	for _, path := range []string{"README.md", "docs/configuration.md", "config.example.yaml"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("Setup: %v", err)
		}
		text := strings.ToLower(strings.Join(strings.Fields(strings.NewReplacer("#", " ", "*", "").Replace(string(raw))), " "))
		for _, want := range ownerLimit {
			if !strings.Contains(text, want) {
				t.Errorf("%s does not say %q about a fine-grained GitHub token", path, want)
			}
		}
	}
}

// tokenSources are the GitHub pages the token choice rests on: the
// fine-grained permissions and their limits, the classic scopes, and the
// missing Checks permission.
var tokenSources = []string{
	"https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens",
	"https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps",
	"https://github.com/orgs/community/discussions/129512",
}

func TestDocs_choosing_a_github_token_cites_github_for_each_claim(t *testing.T) {
	raw, err := os.ReadFile("docs/configuration.md")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	_, section, found := strings.Cut(string(raw), "\n## Choosing a GitHub token\n")
	if !found {
		t.Fatal("docs/configuration.md has no Choosing a GitHub token section")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	for _, url := range tokenSources {
		if !strings.Contains(section, url) {
			t.Errorf("docs/configuration.md Choosing a GitHub token does not cite %s", url)
		}
	}
	for _, want := range []string{"fine-grained", "`repo`", "write access", "expir", "one resource owner"} {
		if !strings.Contains(section, want) {
			t.Errorf("docs/configuration.md Choosing a GitHub token does not name %q", want)
		}
	}
}
