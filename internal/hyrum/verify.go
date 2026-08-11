package hyrum

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/git-pkgs/managers"
)

// errorTailBytes bounds how much of a failed test-runner's combined output
// is kept in VerifyResult.Error when the output could not be parsed at all.
const errorTailBytes = 500

// maxVerifyOutput bounds VerifyResult.Output. It is large enough to hold the
// assertion diffs the validate step reads while keeping meta.json bounded.
const maxVerifyOutput = 16 << 10

// VerifyResult is the outcome of running generated tests against one
// dependency version.
type VerifyResult struct {
	Version string   `json:"version"`
	Pass    int      `json:"pass"`
	Fail    int      `json:"fail"`
	Failed  []string `json:"failed,omitempty"`
	// Output is the test runner's combined stdout/stderr, truncated to
	// maxVerifyOutput bytes from the tail so failure detail near the end of a
	// long run is retained. Populated only when Fail > 0 or Error is set.
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// TestCommand returns the argv that runs test files under dir for the given
// purl ecosystem type. files is the pre-globbed list of test file paths for
// runners that need explicit paths rather than a directory.
type TestCommand func(dir string, files []string) []string

// VerifyMatrix installs dep at each of versions in a fresh scratch directory,
// runs the supplied test files there, and returns one VerifyResult per
// version. mgr must be a package manager whose ecosystem matches the tests
// (npm for .js files, go for .go, ...); it is detected against scratch after
// Init so an existing manifest is present.
func VerifyMatrix(ctx context.Context, mgr managers.Manager, cmd TestCommand, scratch, depName string, files []GeneratedFile, versions []string) []VerifyResult {
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return []VerifyResult{{Error: err.Error()}}
	}
	written, err := WriteFiles(scratch, files)
	if err != nil {
		return []VerifyResult{{Error: fmt.Sprintf("write test files: %v", err)}}
	}
	rel := make([]string, len(written))
	for i, w := range written {
		rel[i], _ = filepath.Rel(scratch, w)
	}

	if r, err := mgr.Init(ctx); err != nil || (r != nil && !r.Success()) {
		msg := ""
		if r != nil {
			msg = r.Stderr
		}
		return []VerifyResult{{Error: fmt.Sprintf("%s init: %v %s", mgr.Name(), err, msg)}}
	}

	var out []VerifyResult
	for _, v := range versions {
		if v == "" {
			continue
		}
		out = append(out, verifyOne(ctx, mgr, cmd, scratch, depName, v, rel))
	}
	return out
}

func verifyOne(ctx context.Context, mgr managers.Manager, tc TestCommand, scratch, depName, version string, files []string) VerifyResult {
	res := VerifyResult{Version: version}
	if r, err := mgr.Add(ctx, depName, managers.AddOptions{Version: version, Exact: true}); err != nil || !r.Success() {
		res.Error = fmt.Sprintf("install %s@%s: %v %s", depName, version, err, strings.TrimSpace(r.Stderr))
		return res
	}
	argv := tc(".", files)
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Dir = scratch
	output, runErr := c.CombinedOutput()
	out := string(output)
	res.Pass, res.Fail, res.Failed = parseTestOutput(out)
	if runErr != nil && res.Fail == 0 && res.Pass == 0 {
		res.Error = fmt.Sprintf("%s: %v: %s", argv[0], runErr, tail(out, errorTailBytes))
	}
	if res.Fail > 0 || res.Error != "" {
		res.Output = tail(out, maxVerifyOutput)
	}
	return res
}

// parseTestOutput extracts pass/fail counts and failing test names from
// common test-runner output. It matches node:test's tap-like summary,
// pytest's short summary, and go test's --- FAIL lines. Unrecognised output
// leaves counts at zero and Error is set by the caller.
var (
	reNodePass = regexp.MustCompile(`(?m)^.?\s*pass\s+(\d+)`)
	reNodeFail = regexp.MustCompile(`(?m)^.?\s*fail\s+(\d+)`)
	reNodeName = regexp.MustCompile(`(?m)^[✖x]\s+(.+?)\s+\(`)
	rePytest   = regexp.MustCompile(`(\d+)\s+passed|(\d+)\s+failed`)
	rePyName   = regexp.MustCompile(`(?m)^FAILED\s+(\S+)`)
	reGoFail   = regexp.MustCompile(`(?m)^--- FAIL:\s+(\S+)`)
	reGoPass   = regexp.MustCompile(`(?m)^--- PASS:\s+(\S+)`)
)

func parseTestOutput(out string) (pass, fail int, failed []string) {
	if m := reNodePass.FindStringSubmatch(out); m != nil {
		pass = atoi(m[1])
	}
	if m := reNodeFail.FindStringSubmatch(out); m != nil {
		fail = atoi(m[1])
	}
	for _, m := range reNodeName.FindAllStringSubmatch(out, -1) {
		failed = append(failed, m[1])
	}
	if pass == 0 && fail == 0 {
		for _, m := range rePytest.FindAllStringSubmatch(out, -1) {
			if m[1] != "" {
				pass = atoi(m[1])
			}
			if m[2] != "" {
				fail = atoi(m[2])
			}
		}
		for _, m := range rePyName.FindAllStringSubmatch(out, -1) {
			failed = append(failed, m[1])
		}
	}
	if pass == 0 && fail == 0 {
		pass = len(reGoPass.FindAllString(out, -1))
		for _, m := range reGoFail.FindAllStringSubmatch(out, -1) {
			failed = append(failed, m[1])
		}
		fail = len(failed)
	}
	return pass, fail, dedup(failed)
}

func atoi(s string) int {
	// Callers pass regex \d+ captures, so the input is always decimal digits.
	n, _ := strconv.Atoi(s)
	return n
}

func dedup(in []string) []string {
	if len(in) < 2 {
		return in
	}
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
