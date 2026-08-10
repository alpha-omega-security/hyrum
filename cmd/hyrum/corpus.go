package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alpha-omega-security/harness"
	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/git-pkgs/clone"
	"github.com/git-pkgs/registries"
)

// cmdCorpus generates Hyrum's tests for one upstream dependency from the
// perspective of several dependent repositories, aggregating the output into
// a single tests/hyrum/<upstream>/from_<dependent>/ tree that the upstream's
// maintainers can run as one suite.
func cmdCorpus(args []string) error {
	fs := newFlags("corpus")
	upstream := fs.String("upstream", "", "upstream dependency name as it appears in dependents' manifests (required)")
	var dependents stringList
	fs.Var(&dependents, "dependent", "dependent repository URL (repeatable)")
	out := fs.String("out", "", "output directory for the aggregated corpus (required)")
	work := fs.String("work", filepath.Join(os.TempDir(), "hyrum-corpus"), "working directory for clones and skill workspaces")
	backend := fs.String("backend", "claude", "harness backend: "+harness.Names())
	run := fs.Bool("run", false, "invoke the backend (otherwise stage only)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *upstream == "" {
		return fmt.Errorf("--upstream is required")
	}
	if *out == "" {
		return fmt.Errorf("--out is required")
	}
	if len(dependents) == 0 {
		return fmt.Errorf("at least one --dependent is required (auto-discovery pending git-pkgs/downstream#7)")
	}

	h, err := harness.ByName(*backend)
	if err != nil {
		return err
	}
	ctx := context.Background()
	p := &pipeline{
		h:       h,
		rc:      registries.DefaultClient(),
		work:    *work,
		outRoot: *out,
		run:     *run,
	}

	for _, spec := range dependents {
		url, ref := splitDependentSpec(spec)
		fmt.Fprintf(os.Stderr, "▶ dependent %s\n", spec)
		dir := filepath.Join(*work, "targets", slugify(url))
		if err := clone.Ensure(ctx, clone.Retry{}, url, dir, ref, true); err != nil {
			fmt.Fprintf(os.Stderr, "  clone: %v\n", err)
			continue
		}
		t, err := hyrum.Analyze(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  analyze: %v\n", err)
			continue
		}
		d, ok := findDep(t, *upstream)
		if !ok {
			fmt.Fprintf(os.Stderr, "  %s does not use %s; skipping\n", url, *upstream)
			continue
		}
		if err := p.genAll(ctx, t, []hyrum.Dep{d}); err != nil {
			fmt.Fprintf(os.Stderr, "  %v\n", err)
		}
	}
	return nil
}

// splitDependentSpec parses "url" or "url@ref". The @ is taken from the end
// so ssh URLs (git@host:path) are left intact when no ref is given.
func splitDependentSpec(s string) (url, ref string) {
	if i := lastAt(s); i > 0 && i > indexOfScheme(s) {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func lastAt(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '@' {
			return i
		}
	}
	return -1
}

// indexOfScheme returns the position after "://" or after "git@", so an @ in
// the auth/scheme portion is not mistaken for a ref separator.
func indexOfScheme(s string) int {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == ':' && s[i+1] == '/' && s[i+2] == '/' {
			return i + 3
		}
	}
	if len(s) > 4 && s[:4] == "git@" {
		return 4
	}
	return 0
}

func slugify(url string) string {
	s := url
	for _, pre := range []string{"https://", "http://", "git@"} {
		if len(s) > len(pre) && s[:len(pre)] == pre {
			s = s[len(pre):]
		}
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '/' || c == ':' || c == '.' {
			c = '_'
		}
		out = append(out, c)
	}
	return string(out)
}
