package github

import (
	"regexp"
	"strings"
)

var repoName = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9_.-]+$`)

func ValidRepo(name string) bool {
	return repoName.MatchString(name) && !strings.HasSuffix(name, "/.") && !strings.HasSuffix(name, "/..")
}
