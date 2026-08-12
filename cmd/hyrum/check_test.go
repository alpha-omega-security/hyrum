package main

import (
	"context"
	"testing"
)

func TestSplitDepSpec(t *testing.T) {
	cases := []struct{ in, name, ver string }{
		{"ws", "ws", ""},
		{"ws@8.17.1", "ws", "8.17.1"},
		{"@scope/pkg", "@scope/pkg", ""},
		{"@scope/pkg@1.2.3", "@scope/pkg", "1.2.3"},
		{"flask@2.3.3", "flask", "2.3.3"},
	}
	for _, c := range cases {
		n, v := splitDepSpec(c.in)
		if n != c.name || v != c.ver {
			t.Errorf("%q: got (%q,%q) want (%q,%q)", c.in, n, v, c.name, c.ver)
		}
	}
}

func TestCheckOneRejectsUnsafeDependencyPath(t *testing.T) {
	if _, err := checkOne(context.Background(), nil, nil, t.TempDir(), "../../escape"); err == nil {
		t.Fatal("checkOne accepted a dependency name that escapes the tests root")
	}
}
