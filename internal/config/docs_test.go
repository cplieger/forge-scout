package config

import (
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// yamlKeys is the keys a file struct decodes, sorted.
func yamlKeys(v any) []string {
	var out []string
	for f := range reflect.TypeOf(v).Fields() {
		if k, _, _ := strings.Cut(f.Tag.Get("yaml"), ","); k != "" {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

var keyCell = regexp.MustCompile("^\\| `([a-z_]+)` \\|")

// tableKeys is the first-column keys of the README table under lead, sorted.
func tableKeys(t *testing.T, readme, lead string) []string {
	t.Helper()
	_, rest, found := strings.Cut(readme, lead+"\n")
	if !found {
		t.Fatalf("README.md has no line %q", lead)
	}
	var out []string
	for l := range strings.SplitSeq(strings.TrimLeft(rest, "\n"), "\n") {
		if !strings.HasPrefix(l, "|") {
			break
		}
		if m := keyCell.FindStringSubmatch(l); m != nil {
			out = append(out, m[1])
		}
	}
	slices.Sort(out)
	return out
}

func readDoc(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	return string(raw)
}

func TestReadme_key_tables_are_the_keys_the_file_takes(t *testing.T) {
	readme := readDoc(t, "../../README.md")
	for _, tc := range []struct {
		lead string
		file any
	}{
		{"The file takes these keys:", fileConfig{}},
		{"Each connection takes these keys:", fileConnection{}},
	} {
		if got, want := tableKeys(t, readme, tc.lead), yamlKeys(tc.file); !slices.Equal(got, want) {
			t.Errorf("README.md table %q = %v, want the keys the file takes, %v", tc.lead, got, want)
		}
	}
}

func TestConfigurationDoc_names_every_exclude_condition(t *testing.T) {
	page := readDoc(t, "../../docs/configuration.md")
	_, section, found := strings.Cut(page, "## Leaving repositories and items out\n")
	if !found {
		t.Fatal("docs/configuration.md has no section Leaving repositories and items out")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	for _, k := range yamlKeys(fileExclude{}) {
		if !strings.Contains(section, "`"+k+"`") {
			t.Errorf("docs/configuration.md exclusions section does not name the rule condition `%s`", k)
		}
	}
}

func TestConfigurationDoc_exclude_example_loads_with_the_rules_it_describes(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	page := readDoc(t, "../../docs/configuration.md")
	_, section, _ := strings.Cut(page, "## Leaving repositories and items out\n")
	_, block, found := strings.Cut(section, "```yaml\n")
	block, _, closed := strings.Cut(block, "```")
	if !found || !closed {
		t.Fatal("docs/configuration.md exclusions section has no yaml example")
	}
	cfg, _, err := Load(writeConfig(t, block))
	if err != nil {
		t.Fatalf("Load(the exclusions example) = %v, want it loaded", err)
	}
	c := cfg.Connections[0]
	for _, tc := range []struct {
		name string
		item Item
		want bool
	}{
		{"renovate_app", Item{Kind: KindIssue, Author: "renovate[bot]"}, true},
		{"dependency_pull_request", Item{Kind: KindPullRequest, Author: "someone", Labels: []string{"dependencies"}}, true},
		{"dependency_issue", Item{Kind: KindIssue, Author: "someone", Labels: []string{"dependencies"}}, false},
		{"draft", Item{Kind: KindPullRequest, Author: "someone", Draft: true}, true},
		{"other", Item{Kind: KindPullRequest, Author: "someone"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := c.Excluded(&tc.item); got != tc.want {
				t.Errorf("the exclusions example leaves out %+v = %t, want %t", tc.item, got, tc.want)
			}
		})
	}
}
