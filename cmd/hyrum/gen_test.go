package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alpha-omega-security/harness"
	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/git-pkgs/brief"
)

func TestAnyRan(t *testing.T) {
	if anyRan(nil) {
		t.Error("nil slice")
	}
	if anyRan([]hyrum.VerifyResult{{Error: "install failed"}}) {
		t.Error("error-only should not count as ran")
	}
	if !anyRan([]hyrum.VerifyResult{{Version: "1.0", Pass: 3}}) {
		t.Error("pass counts as ran")
	}
	if !anyRan([]hyrum.VerifyResult{{Error: "x"}, {Version: "2.0", Fail: 1}}) {
		t.Error("mixed: one ran")
	}
}

func TestAddBackendRecoveries(t *testing.T) {
	meta := map[string]any{}
	addBackendRecoveries(meta, nil)
	if len(meta) != 0 {
		t.Fatalf("clean run changed metadata: %v", meta)
	}

	addBackendRecoveries(meta, []string{"hyrum-usage", "hyrum-validate"})
	if got := meta["recovered_output"]; got != true {
		t.Errorf("recovered_output = %v", got)
	}
	steps, ok := meta["recovered_steps"].([]string)
	if !ok || len(steps) != 2 || steps[0] != "hyrum-usage" || steps[1] != "hyrum-validate" {
		t.Errorf("recovered_steps = %v", meta["recovered_steps"])
	}
	if _, ok := meta["backend_error"]; ok {
		t.Error("raw backend error must not be persisted")
	}
}

func TestGenOneRejectsUnsafeDependencyPath(t *testing.T) {
	p := &pipeline{}
	target := &hyrum.Target{
		Path:   filepath.Join(t.TempDir(), "target"),
		Report: &brief.Report{},
	}
	dep := hyrum.Dep{Name: "../../escape", Ecosystem: hyrum.EcoNPM}

	if err := p.genOne(t.Context(), target, nil, dep); err == nil {
		t.Fatal("genOne accepted a dependency name that escapes the work and output roots")
	}
}

func TestGenAllReturnsDependencyFailures(t *testing.T) {
	targetDir := newGitTarget(t)
	target := &hyrum.Target{Path: targetDir, Report: &brief.Report{}}
	p := &pipeline{}
	deps := []hyrum.Dep{{Name: "../../escape", Ecosystem: hyrum.EcoNPM}}

	err := p.genAll(t.Context(), target, deps)
	if err == nil {
		t.Fatal("genAll discarded a dependency generation failure")
	}
	if !strings.Contains(err.Error(), `../../escape: unsafe dependency name "../../escape"`) {
		t.Fatalf("genAll error = %q", err)
	}
}

func TestPrepareWorkspaceRemovesTransientArtifacts(t *testing.T) {
	ws := t.TempDir()
	for _, name := range transientWorkspaceFiles {
		if err := os.WriteFile(filepath.Join(ws, name), []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	depDir := filepath.Join(ws, "dep")
	if err := os.Mkdir(depDir, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(depDir, "keep")
	if err := os.WriteFile(keep, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := prepareWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	for _, name := range transientWorkspaceFiles {
		if _, err := os.Stat(filepath.Join(ws, name)); !os.IsNotExist(err) {
			t.Errorf("transient artifact %q remains: %v", name, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("reusable dependency clone was removed: %v", err)
	}
}

func newGitTarget(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "README.md"}, {"commit", "-q", "-m", "fixture"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

type recoveredRunner struct {
	output json.RawMessage
}

func (r recoveredRunner) RunSkill(context.Context, harness.Harness, string, string, string) (*hyrum.RunResult, error) {
	output := r.output
	if output == nil {
		output = json.RawMessage(`{"verdicts":[{"test":"t","status":"weak","action":"strengthen","reasoning":"r"}]}`)
	}
	return &hyrum.RunResult{
		Output:       output,
		CostUSD:      0.25,
		BackendError: "backend exited non-zero after writing fresh output",
	}, nil
}

func TestRunValidateReturnsRecovery(t *testing.T) {
	p := &pipeline{h: harness.ClaudeHarness{}}
	out, cost, recovery, err := p.runValidate(context.Background(), recoveredRunner{}, t.TempDir(), []hyrum.VerifyResult{{Pass: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || len(out.Verdicts) != 1 {
		t.Fatalf("validate output = %+v", out)
	}
	if cost != 0.25 {
		t.Errorf("cost = %v", cost)
	}
	if recovery == "" {
		t.Fatal("validate recovery was discarded")
	}
}

func TestRunValidateDecodeFailureDoesNotReturnRecovery(t *testing.T) {
	p := &pipeline{h: harness.ClaudeHarness{}}
	out, _, recovery, err := p.runValidate(
		context.Background(),
		recoveredRunner{output: json.RawMessage(`{"verdicts":"bad"}`)},
		t.TempDir(),
		[]hyrum.VerifyResult{{Pass: 1}},
	)
	if err == nil {
		t.Fatal("want decode error")
	}
	if out != nil {
		t.Fatalf("discarded validation output = %+v", out)
	}
	if recovery != "" {
		t.Fatalf("discarded output reported as recovered: %q", recovery)
	}
}
