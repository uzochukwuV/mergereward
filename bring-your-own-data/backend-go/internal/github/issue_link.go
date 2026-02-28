package github

import (
	"regexp"
	"strconv"
)

var issueRefRe = regexp.MustCompile(`(?i)\b(?:fixes|closes|resolves)\s+#(\d+)\b`)

func ExtractIssueNumber(text string) (int, bool) {
	m := issueRefRe.FindStringSubmatch(text)
	if len(m) != 2 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}
