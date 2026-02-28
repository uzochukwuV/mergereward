package github

import "testing"

func TestExtractIssueNumber(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"Fixes #123", 123, true},
		{"closes #9", 9, true},
		{"Resolves #42 and something", 42, true},
		{"No linkage", 0, false},
		{"fixes #0", 0, false},
	}

	for _, tc := range cases {
		got, ok := ExtractIssueNumber(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("ExtractIssueNumber(%q) = (%d,%v), want (%d,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
