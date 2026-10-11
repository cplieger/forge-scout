package config

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"slices"
	"strconv"
	"strings"
)

// Kind is the kind of open item an exclude rule reads.
type Kind uint8

// An Item is KindPullRequest or KindIssue; a rule of KindAny reads both.
const (
	KindAny Kind = iota
	KindPullRequest
	KindIssue
)

// Item is what an exclude rule reads of one open pull request or issue, each
// string raw as the forge sent it.
type Item struct {
	Repo   string
	Author string
	Title  string
	Labels []string
	Kind   Kind
	Draft  bool
}

// Exclude is one exclude rule. It matches an Item that meets every condition
// it sets: Author, Label and Repo compare ignoring case, Title is an RE2
// expression found anywhere in the title, and Draft reads only pull requests.
type Exclude struct {
	Title  *regexp.Regexp
	Draft  *bool
	Author string
	Label  string
	Repo   string
	// Path names the rule in the file, such as connections[1].exclude[2].
	Path string
	Kind Kind
}

// Matches reports whether it meets every condition e sets.
func (e *Exclude) Matches(it *Item) bool {
	switch {
	case e.Kind != KindAny && e.Kind != it.Kind,
		e.Draft != nil && (it.Kind != KindPullRequest || it.Draft != *e.Draft),
		e.Author != "" && !strings.EqualFold(e.Author, it.Author),
		e.Repo != "" && !strings.EqualFold(e.Repo, it.Repo),
		e.Label != "" && !slices.ContainsFunc(it.Labels, func(l string) bool { return strings.EqualFold(e.Label, l) }),
		e.Title != nil && !e.Title.MatchString(it.Title):
		return false
	}
	return true
}

// Excluded returns the Path of the first of c's rules it matches.
func (c *Connection) Excluded(it *Item) (rule string, ok bool) {
	for i := range c.Exclude {
		if c.Exclude[i].Matches(it) {
			return c.Exclude[i].Path, true
		}
	}
	return "", false
}

// fileExclude is one rule as written. A nil pointer is a condition left out or
// written as a YAML null; excludeRule tells them apart by the source.
type fileExclude struct {
	Kind   *string `yaml:"kind"`
	Author *string `yaml:"author"`
	Label  *string `yaml:"label"`
	Title  *string `yaml:"title"`
	Repo   *string `yaml:"repo"`
	Draft  *bool   `yaml:"draft"`
}

// excludeRules validates the rules of the list at key path at. A rule written
// as a bare - decodes as nil.
func excludeRules(at string, in []*fileExclude, src source) ([]Exclude, []error) {
	out := make([]Exclude, 0, len(in))
	var errs []error
	for i, fe := range in {
		path := at + "[" + strconv.Itoa(i+1) + "]"
		if fe == nil {
			fe = &fileExclude{}
		}
		e, rerrs := excludeRule(path, fe, src)
		for _, err := range rerrs {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
		}
		out = append(out, e)
	}
	return out, errs
}

func excludeRule(path string, fe *fileExclude, src source) (Exclude, []error) {
	e := Exclude{Path: path, Draft: fe.Draft}
	written := func(key string) bool { return src.written(path, key) }
	if *fe == (fileExclude{}) && !slices.ContainsFunc([]string{"kind", "author", "label", "title", "repo", "draft"}, written) {
		return e, []error{errors.New("the rule sets no condition, so it would leave out every item")}
	}
	var errs []error
	cond := func(key string, v *string) string {
		s, err := condition(key, v, written(key))
		if err != nil {
			errs = append(errs, err)
		}
		return s
	}
	switch kind := cond("kind", fe.Kind); kind {
	case "":
	case "pull_request":
		e.Kind = KindPullRequest
	case "issue":
		e.Kind = KindIssue
	default:
		errs = append(errs, fmt.Errorf("kind must be pull_request or issue, not %q", src.show(path+".kind", kind)))
	}
	if e.Kind == KindIssue && e.Draft != nil {
		errs = append(errs, errors.New("draft matches only pull requests, so it cannot be set with kind: issue"))
	}
	if e.Draft == nil && written("draft") {
		errs = append(errs, errors.New("draft is empty"))
	}
	e.Author, e.Label = cond("author", fe.Author), cond("label", fe.Label)
	if e.Repo = cond("repo", fe.Repo); e.Repo != "" && !fullPath(e.Repo) {
		errs = append(errs, errors.New("repo must be a full owner/name path"))
	}
	var err error
	if e.Title, err = titleCondition(fe.Title, written("title")); err != nil {
		errs = append(errs, err)
	}
	return e, errs
}

// condition returns the string condition key as written, padding included and
// never repaired; one written blank or as a YAML null is refused.
func condition(key string, v *string, written bool) (string, error) {
	switch {
	case v == nil && written, v != nil && strings.TrimSpace(*v) == "":
		return "", fmt.Errorf("%s is empty", key)
	case v == nil:
		return "", nil
	}
	return *v, nil
}

// titleCondition compiles the title condition; a title of only spaces is a
// valid expression, so only an empty or null one is refused.
func titleCondition(v *string, written bool) (*regexp.Regexp, error) {
	switch {
	case v == nil && !written:
		return nil, nil
	case v == nil || *v == "":
		return nil, errors.New("title is empty")
	}
	re, err := regexp.Compile(*v)
	if err != nil {
		return nil, titleError(err)
	}
	return re, nil
}

// fullPath reports whether p names a repository under its owner. A GitLab
// project under subgroups has more than two segments.
func fullPath(p string) bool {
	parts := strings.Split(p, "/")
	return len(parts) > 1 && !slices.Contains(parts, "")
}

// written reports whether the rule at path writes key, directly or through a
// YAML merge key.
func (s source) written(path, key string) bool {
	if _, ok := s[path+"."+key]; ok {
		return true
	}
	for p := range s {
		if strings.HasPrefix(p, path+".<<") && strings.HasSuffix(p, "."+key) {
			return true
		}
	}
	return false
}

// titleError names what is wrong with a title expression, never the
// expression, which may hold a value expanded from the environment.
func titleError(err error) error {
	if se, ok := errors.AsType[*syntax.Error](err); ok {
		return fmt.Errorf("title is not a valid regular expression: %s", se.Code)
	}
	return errors.New("title is not a valid regular expression")
}
