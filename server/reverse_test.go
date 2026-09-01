package main

import "testing"

func TestReverse(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"a", "a"},
		{"hello", "olleh"},
		{"héllo", "olléh"},
		{"hi 👋🏼", "🏼👋 ih"},
	}
	for _, c := range cases {
		if got := Reverse(c.in); got != c.want {
			t.Errorf("Reverse(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
