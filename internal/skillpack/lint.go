package skillpack

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// Issue levels. Errors break the Agent Skills spec and block installation. Warnings
// are portability risks. Info explains what a harness-specific field does.
const (
	LevelError = "error"
	LevelWarn  = "warn"
	LevelInfo  = "info"
)

// Issue is one finding.
type Issue struct {
	Level, Msg string
}

// IssuesHaveErrors reports whether any issue is an error.
func IssuesHaveErrors(list []Issue) bool {
	for _, i := range list {
		if i.Level == LevelError {
			return true
		}
	}
	return false
}

var nameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// extras are fields outside the spec, with who honors them. They are fine in project
// skills, since other clients ignore unknown fields, but they are rejected by claude.ai
// uploads and the Skills API.
var extras = map[string]string{
	"disable-model-invocation": "Claude Code, Cursor and Pi; others may invoke the skill on their own, so also say when to use it in description",
	"when_to_use":              "Claude Code",
	"argument-hint":            "Claude Code",
	"arguments":                "Claude Code",
	"user-invocable":           "Claude Code",
	"disallowed-tools":         "Claude Code",
	"model":                    "Claude Code",
	"effort":                   "Claude Code",
	"context":                  "Claude Code",
	"agent":                    "Claude Code",
	"background":               "Claude Code",
	"hooks":                    "Claude Code",
	"shell":                    "Claude Code",
	"paths":                    "Claude Code and Cursor",
	"icon":                     "Cursor",
	"color":                    "Cursor",
}

var specFields = map[string]bool{
	"name": true, "description": true, "license": true,
	"compatibility": true, "metadata": true, "allowed-tools": true,
}

type frontmatter struct {
	raw  map[string]any
	meta map[string]any
	body string
}

func parseText(b []byte) (frontmatter, error) {
	s := string(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")))
	if !strings.HasPrefix(s, "---") {
		return frontmatter{}, errors.New("SKILL.md must start with --- frontmatter")
	}
	rest := s[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return frontmatter{}, errors.New("frontmatter has no closing ---")
	}
	fm := frontmatter{raw: map[string]any{}}
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm.raw); err != nil {
		return fm, fmt.Errorf("frontmatter is not valid YAML (a colon in an unquoted value is the usual cause): %w", err)
	}
	fm.meta, _ = fm.raw["metadata"].(map[string]any)
	fm.body = strings.TrimLeft(rest[end+4:], "\r\n")
	return fm, nil
}

func parseSkill(file string) (frontmatter, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return frontmatter{}, err
	}
	return parseText(b)
}

func parseBundled(name string) (frontmatter, error) {
	b, err := bundled.ReadFile(path.Join("skills", name, "SKILL.md"))
	if err != nil {
		return frontmatter{}, err
	}
	return parseText(b)
}

// Lint checks a skill directory against the Agent Skills specification.
func Lint(dir string) []Issue {
	var out []Issue
	add := func(level, format string, args ...any) {
		out = append(out, Issue{level, fmt.Sprintf(format, args...)})
	}
	fm, err := parseSkill(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		add(LevelError, "%v", err)
		return out
	}

	name, _ := fm.raw["name"].(string)
	base := filepath.Base(dir)
	switch {
	case name == "":
		add(LevelError, "name is required")
	case len(name) > 64:
		add(LevelError, "name is over 64 characters")
	case !nameRE.MatchString(name):
		add(LevelError, "name %q may use only lowercase letters, numbers and single hyphens", name)
	case name != base:
		add(LevelError, "name %q must match its directory %q", name, base)
	}

	desc, _ := fm.raw["description"].(string)
	switch {
	case strings.TrimSpace(desc) == "":
		add(LevelError, "description is required")
	case utf8.RuneCountInString(desc) > 1024:
		add(LevelError, "description is over 1024 characters")
	case utf8.RuneCountInString(desc) < 40:
		add(LevelWarn, "description is very short; say what the skill does and when to use it")
	}
	if strings.Contains(desc, "TODO") {
		add(LevelWarn, "description still has a TODO")
	}
	when, _ := fm.raw["when_to_use"].(string)
	if utf8.RuneCountInString(desc)+utf8.RuneCountInString(when) > 1536 {
		add(LevelWarn, "Claude Code truncates description plus when_to_use past 1536 characters")
	}

	if v, ok := fm.raw["compatibility"]; ok {
		s, isStr := v.(string)
		if !isStr || s == "" || utf8.RuneCountInString(s) > 500 {
			add(LevelError, "compatibility must be 1 to 500 characters")
		}
	}
	if v, ok := fm.raw["allowed-tools"]; ok {
		if _, isStr := v.(string); !isStr {
			add(LevelError, "allowed-tools must be a space-separated string, not a list")
		}
	}
	if v, ok := fm.raw["metadata"]; ok {
		m, isMap := v.(map[string]any)
		if !isMap {
			add(LevelError, "metadata must be a map")
		}
		for k, val := range m {
			if _, isStr := val.(string); !isStr {
				add(LevelError, "metadata.%s must be a string; quote it, as in version: \"1.0\"", k)
			}
		}
		if _, ok := m["version"]; !ok {
			add(LevelInfo, "metadata.version is not set")
		}
	} else {
		add(LevelInfo, "no metadata; a version helps track updates")
	}
	for k := range fm.raw {
		if specFields[k] {
			continue
		}
		if who, known := extras[k]; known {
			add(LevelInfo, "%s is honored by %s, ignored by other harnesses, and rejected by claude.ai uploads", k, who)
		} else {
			add(LevelWarn, "unknown field %s; put your own data under metadata", k)
		}
	}
	if lines := strings.Count(fm.body, "\n") + 1; lines > 500 {
		add(LevelWarn, "body is %d lines; the spec recommends under 500, with details in references/", lines)
	}
	if strings.TrimSpace(fm.body) == "" {
		add(LevelWarn, "body is empty")
	}
	return out
}
