package github

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBodyRef(t *testing.T) {
	for body, want := range map[string]string{
		"Metronome: MET-17":                             "MET-17",
		"  metronome:  MET-17  ":                        "MET-17",
		"```\nMetronome: other\n```":                    "",
		"Text Metronome: MET-17":                        "",
		"Metronome: first\nMetronome: second":           "first",
		"<!-- Metronome: hidden -->\nMetronome: MET-17": "MET-17",
	} {
		if got := bodyRef(body); got != want {
			t.Errorf("%q: %q != %q", body, got, want)
		}
	}
}

func TestBodyIntent(t *testing.T) {
	for body, want := range map[string]string{
		"": "", "# Title\nText here": "Text here",
		"<!-- template -->\r\nFirst line\r\nsecond line\r\n\r\nNext": "First line second line",
		"Metronome: MET-17\nFirst paragraph":                         "First paragraph",
		"- [ ] one\n  continuation\n\nText here":                     "Text here",
		"## Acceptance criteria\n- one\n## Why\nText here":           "Text here",
		"~~~md\nIgnore me\n~~~\nText here":                           "Text here",
	} {
		if got := bodyIntent(body); got != want {
			t.Errorf("%q: %q != %q", body, got, want)
		}
	}
	long := bodyIntent(strings.Repeat("word ", 120))
	if utf8.RuneCountInString(long) > 500 || !strings.HasSuffix(long, "word…") {
		t.Fatal(long)
	}
}

func TestBodyCriteria(t *testing.T) {
	for _, test := range []struct {
		body string
		want []string
	}{
		{"- [ ] elsewhere\n## Acceptance criteria\n- one\n  more text\n1. two\n  - [x] nested\n### Detail\n+ three\n## Other\n- not included", []string{"one more text", "two", "nested", "three"}},
		{"### Acceptance Criteria:\n1) criterion", []string{"criterion"}},
		{"- [ ] one\n* [x] two\n  wrapped\n- not a task\n```\n- [ ] hidden\n```", []string{"one", "two wrapped"}},
		{"Prose only", []string{}},
	} {
		if got := bodyCriteria(test.body); !slices.Equal(got, test.want) {
			t.Errorf("%q: %v != %v", test.body, got, test.want)
		}
	}
}

func TestBodyPreservesRefAndCriteriaAfterMarkdown(t *testing.T) {
	for _, test := range []struct {
		name, prefix, intent string
	}{
		{"inline comment opener", "Strip `<!--` from templates.", "Strip `<!--` from templates."},
		{"fenced comment opener", "```html\n<!--\n```\nKeep the template.", "Keep the template."},
		{"inline backtick span", "```go``` is the language.", "```go``` is the language."},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := test.prefix + "\n\nMetronome: MET-17\n\n## Acceptance criteria\n- Preserve the record."
			if got := bodyRef(body); got != "MET-17" {
				t.Errorf("ref = %q, want MET-17", got)
			}
			if got := bodyCriteria(body); !slices.Equal(got, []string{"Preserve the record."}) {
				t.Errorf("criteria = %v, want [Preserve the record.]", got)
			}
			if got := bodyIntent(body); got != test.intent {
				t.Errorf("intent = %q, want %q", got, test.intent)
			}
		})
	}
}
