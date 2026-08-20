package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/alpha-omega-security/harness"
	hyrumconfig "github.com/alpha-omega-security/hyrum/internal/config"
	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/alpha-omega-security/hyrum/internal/hyrum/usage"
	"github.com/git-pkgs/brief"
)

type runnerCall struct {
	name     string
	model    string
	maxTurns int
}

type recordingRunner struct {
	calls []runnerCall
}

func (r *recordingRunner) RunSkill(_ context.Context, _ harness.Harness, _, name, _ string, opts hyrum.RunOptions) (*hyrum.RunResult, error) {
	r.calls = append(r.calls, runnerCall{name: name, model: opts.Model, maxTurns: opts.MaxTurns})
	output := json.RawMessage(`{"files":[]}`)
	if name == "hyrum-validate" {
		output = json.RawMessage(`{"verdicts":[]}`)
	}
	return &hyrum.RunResult{Output: output}, nil
}

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

func TestNewPipelineIndexesProductionUsageByDefault(t *testing.T) {
	p := newPipeline(harness.ClaudeHarness{}, t.TempDir(), t.TempDir(), false, "")
	want := []usage.Scope{usage.ScopeProduction}
	if !reflect.DeepEqual(p.usageOptions.Scopes, want) {
		t.Errorf("usage scopes = %v, want %v", p.usageOptions.Scopes, want)
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
	for _, name := range transientWorkspaceDirs {
		if err := os.Mkdir(filepath.Join(ws, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(ws, name, "stale"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := prepareWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	for _, name := range transientWorkspaceFiles {
		if _, err := os.Stat(filepath.Join(ws, name)); !os.IsNotExist(err) {
			t.Errorf("transient artifact %q remains: %v", name, err)
		}
	}
	for _, name := range transientWorkspaceDirs {
		if _, err := os.Stat(filepath.Join(ws, name)); !os.IsNotExist(err) {
			t.Errorf("transient directory %q remains: %v", name, err)
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

func (r recoveredRunner) RunSkill(context.Context, harness.Harness, string, string, string, hyrum.RunOptions) (*hyrum.RunResult, error) {
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

func TestResolveGenOptionsPrecedence(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "hyrum.yaml")
	target := t.TempDir()
	backend, configOut, configWork := "codex", "configured/out", "configured/work"
	configOutlineBytes := 131072
	cfg := hyrumconfig.File{Backend: &backend, Out: &configOut, Work: &configWork, OutlineBytes: &configOutlineBytes}

	t.Run("discovered config cannot set work", func(t *testing.T) {
		got, err := resolveGenOptions(target, configPath, false, cfg, defaultGenOptions(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.backend != "codex" {
			t.Errorf("backend = %q", got.backend)
		}
		if got.out != filepath.Join(target, configOut) {
			t.Errorf("out = %q", got.out)
		}
		if got.work != defaultGenOptions().work {
			t.Errorf("work = %q", got.work)
		}
		if got.outlineBytes != configOutlineBytes {
			t.Errorf("outline bytes = %d", got.outlineBytes)
		}
	})

	t.Run("explicit config can set work", func(t *testing.T) {
		got, err := resolveGenOptions(target, configPath, true, cfg, defaultGenOptions(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.work != filepath.Join(configDir, configWork) {
			t.Errorf("work = %q", got.work)
		}
	})

	t.Run("explicit flags over config", func(t *testing.T) {
		cli := defaultGenOptions()
		cli.backend = "claude" // Deliberately equal to the built-in default.
		cli.out = "cli/out"
		cli.work = "cli/work"
		cli.outlineBytes = 65536
		set := map[string]bool{"backend": true, "out": true, "work": true, "outline-bytes": true}
		got, err := resolveGenOptions(target, configPath, false, cfg, cli, set)
		if err != nil {
			t.Fatal(err)
		}
		if got.backend != "claude" || got.out != filepath.Join(target, "cli/out") || got.work != "cli/work" || got.outlineBytes != 65536 {
			t.Fatalf("resolved = %+v", got)
		}
	})
}

func TestResolveGenOptionsExpandsConfiguredOutFromTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := t.TempDir()
	configured := "~/hyrum-output"
	cfg := hyrumconfig.File{Out: &configured}
	got, err := resolveGenOptions(target, filepath.Join(t.TempDir(), "hyrum.yaml"), true, cfg, defaultGenOptions(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.out != filepath.Join(home, "hyrum-output") {
		t.Fatalf("out = %q", got.out)
	}
}

func TestResolveGenOptionsConfinesDiscoveredOutToTarget(t *testing.T) {
	target := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	cfg := hyrumconfig.File{Out: &outside}
	_, err := resolveGenOptions(target, filepath.Join(target, "hyrum.yaml"), false, cfg, defaultGenOptions(), nil)
	if err == nil || !strings.Contains(err.Error(), "automatic output") {
		t.Fatalf("error = %v, want discovered-config confinement error", err)
	}
}

func TestResolveGenOptionsRejectsDiscoveredOutThroughSymlink(t *testing.T) {
	target := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(target, "redirect")); err != nil {
		t.Fatal(err)
	}
	configured := "redirect/generated"
	cfg := hyrumconfig.File{Out: &configured}
	_, err := resolveGenOptions(target, filepath.Join(target, "hyrum.yaml"), false, cfg, defaultGenOptions(), nil)
	if err == nil || !strings.Contains(err.Error(), "automatic output") {
		t.Fatalf("error = %v, want symlink confinement error", err)
	}
}

func TestResolveGenOptionsRejectsDefaultOutThroughSymlink(t *testing.T) {
	target := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(target, "tests")); err != nil {
		t.Fatal(err)
	}
	_, err := resolveGenOptions(target, "", false, hyrumconfig.File{}, defaultGenOptions(), nil)
	if err == nil || !strings.Contains(err.Error(), "automatic output") {
		t.Fatalf("error = %v, want default-output confinement error", err)
	}
}

func TestPathWithinResolved(t *testing.T) {
	root := t.TempDir()
	if !pathWithinResolved(root, filepath.Join(root, "not", "created")) {
		t.Fatal("missing child should remain inside root")
	}
	if pathWithinResolved(root, filepath.Join(root, "..", "outside")) {
		t.Fatal("parent traversal should escape root")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if pathWithinResolved(root, filepath.Join(root, "link", "child")) {
		t.Fatal("symlinked child should escape root")
	}
	danglingTarget := filepath.Join(t.TempDir(), "created-later")
	if err := os.Symlink(danglingTarget, filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	if pathWithinResolved(root, filepath.Join(root, "dangling", "child")) {
		t.Fatal("dangling symlink should fail closed")
	}
}

func TestGenOneRejectsResolvedPathEscapes(t *testing.T) {
	target := &hyrum.Target{Path: t.TempDir(), Report: &brief.Report{}}
	targetDir := filepath.Base(target.Path)
	dep := hyrum.Dep{Name: "ws", Ecosystem: "npm"}

	t.Run("work root", func(t *testing.T) {
		p := &pipeline{work: t.TempDir(), outRoot: t.TempDir()}
		if err := os.Symlink(t.TempDir(), filepath.Join(p.work, targetDir)); err != nil {
			t.Fatal(err)
		}
		err := p.genOne(context.Background(), target, nil, dep)
		if err == nil || !strings.Contains(err.Error(), "outside work root") {
			t.Fatalf("error = %v, want work-root confinement error", err)
		}
	})

	t.Run("output root", func(t *testing.T) {
		p := &pipeline{work: t.TempDir(), outRoot: t.TempDir()}
		if err := os.Symlink(t.TempDir(), filepath.Join(p.outRoot, dep.Name)); err != nil {
			t.Fatal(err)
		}
		err := p.genOne(context.Background(), target, nil, dep)
		if err == nil || !strings.Contains(err.Error(), "outside output root") {
			t.Fatalf("error = %v, want output-root confinement error", err)
		}
	})
}

func TestResolveGenOptionsExplicitOutOverridesUnsafeDiscoveredOut(t *testing.T) {
	target := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	cfg := hyrumconfig.File{Out: &outside}
	cli := defaultGenOptions()
	cli.out = "safe-output"
	got, err := resolveGenOptions(target, filepath.Join(target, "hyrum.yaml"), false, cfg, cli, map[string]bool{"out": true})
	if err != nil {
		t.Fatal(err)
	}
	if got.out != filepath.Join(target, "safe-output") {
		t.Fatalf("out = %q", got.out)
	}
}

func TestCmdGenRejectsFlagsAfterTarget(t *testing.T) {
	target := t.TempDir()
	err := cmdGen(context.Background(), []string{target, "--config", filepath.Join(t.TempDir(), "missing.yaml")})
	if err == nil || !strings.Contains(err.Error(), "unexpected arguments after <path>") {
		t.Fatalf("error = %v", err)
	}
}

func TestCmdGenValidatesSymbolAndBatchSelectionBeforeAnalysis(t *testing.T) {
	if err := cmdGen(t.Context(), []string{"--symbol", "Session", t.TempDir()}); err == nil || !strings.Contains(err.Error(), "--symbol requires exactly one --dep") {
		t.Fatalf("symbol error = %v", err)
	}
	if err := cmdGen(t.Context(), []string{"--batch-size", "-1", t.TempDir()}); err == nil || !strings.Contains(err.Error(), "--batch-size must be zero or greater") {
		t.Fatalf("batch error = %v", err)
	}
	if err := cmdGen(t.Context(), []string{"--batch-sites", "-1", t.TempDir()}); err == nil || !strings.Contains(err.Error(), "--batch-sites must be zero or greater") {
		t.Fatalf("batch sites error = %v", err)
	}
	if err := cmdGen(t.Context(), []string{"--outline-bytes", "0", t.TempDir()}); err == nil || !strings.Contains(err.Error(), "--outline-bytes must be greater than zero") {
		t.Fatalf("outline bytes error = %v", err)
	}
}

func TestCmdGenConfigFileBehavior(t *testing.T) {
	target := t.TempDir()

	t.Run("missing explicit", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing.yaml")
		err := cmdGen(context.Background(), []string{"--config", missing, target})
		if err == nil || !strings.Contains(err.Error(), "read config") || !strings.Contains(err.Error(), missing) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("missing automatic is optional", func(t *testing.T) {
		err := cmdGen(context.Background(), []string{target})
		if err == nil || !strings.Contains(err.Error(), "no dependencies selected") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestVisitedFlagsRecordsExplicitDefaults(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("backend", "claude", "")
	if err := fs.Parse([]string{"--backend", "claude"}); err != nil {
		t.Fatal(err)
	}
	if !visitedFlags(fs)["backend"] {
		t.Fatal("explicit default-valued flag was not recorded")
	}
}

func TestSelectGenDepsAppliesOverridesWithoutMutatingTarget(t *testing.T) {
	target := &hyrum.Target{Deps: []hyrum.Dep{
		{Name: "ws", PURL: "pkg:npm/ws", Version: "^7.4.2", Direct: true},
		{Name: "lodash", PURL: "pkg:npm/lodash", Version: "4.17.20", Direct: true},
	}}
	baseline := "8.17.1"
	skip := true
	overrides := map[string]hyrumconfig.Dependency{
		"ws":             {Baseline: &baseline},
		"pkg:npm/lodash": {Skip: &skip},
	}

	got := selectGenDeps(target, nil, overrides)
	if len(got) != 1 || got[0].Name != "ws" || got[0].Version != baseline {
		t.Fatalf("default selection = %+v", got)
	}
	if target.Deps[0].Version != "^7.4.2" {
		t.Fatalf("target dependency mutated: %+v", target.Deps[0])
	}

	got = selectGenDeps(target, stringList{"pkg:npm/lodash"}, overrides)
	if len(got) != 1 || got[0].Name != "lodash" {
		t.Fatalf("explicit --dep should override skip: %+v", got)
	}
}

func TestSelectGenDepsOverrideByPURL(t *testing.T) {
	target := &hyrum.Target{Deps: []hyrum.Dep{{
		Name: "ws", PURL: "pkg:npm/ws", Version: "7.4.2", Direct: true,
	}}}
	baseline := "8.0.0"
	got := selectGenDeps(target, nil, map[string]hyrumconfig.Dependency{
		"pkg:npm/ws": {Baseline: &baseline},
	})
	if len(got) != 1 || got[0].Version != baseline {
		t.Fatalf("selection = %+v", got)
	}
}

func TestResolveModelsUsesHarnessTier(t *testing.T) {
	h := harness.CodexHarness{}
	got, err := resolveModels(h, map[string]string{
		"hyrum-usage":    "mid",
		"hyrum-generate": "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, model := range h.DefaultModels() {
		want[model.Tier] = model.ID
	}
	if want["mid"] == "" || want["high"] == "" {
		t.Fatalf("Codex defaults lack required tiers: %v", h.DefaultModels())
	}
	if got["hyrum-usage"] != want["mid"] || got["hyrum-generate"] != want["high"] {
		t.Fatalf("models = %v, want mid=%q high=%q", got, want["mid"], want["high"])
	}
}

type checkoutFixture struct {
	dir    string
	marker string
}

func newCheckoutFixture(t *testing.T) checkoutFixture {
	t.Helper()
	fixture := checkoutFixture{dir: t.TempDir()}
	fixture.marker = filepath.Join(fixture.dir, "api.txt")
	fixture.git(t, "init", "-q")
	fixture.write(t, "v1.0.0")
	fixture.git(t, "add", ".")
	fixture.git(t, "commit", "-q", "-m", "one")
	fixture.git(t, "tag", "v1.0.0")
	fixture.write(t, "v1.1.0")
	fixture.git(t, "commit", "-q", "-am", "two")
	fixture.git(t, "tag", "1.1.0")
	fixture.write(t, "head")
	fixture.git(t, "commit", "-q", "-am", "three")
	return fixture
}

func (f checkoutFixture) git(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", f.dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (f checkoutFixture) write(t *testing.T, value string) {
	t.Helper()
	if err := os.WriteFile(f.marker, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f checkoutFixture) read(t *testing.T) string {
	t.Helper()
	contents, err := os.ReadFile(f.marker)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func (f checkoutFixture) branch(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("git", "-C", f.dir, "symbolic-ref", "--short", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestCheckoutVersionRestoresAfterCancellation(t *testing.T) {
	fixture := newCheckoutFixture(t)
	checkoutCtx, cancel := context.WithCancel(context.Background())
	restore, tag, matched := checkoutVersion(checkoutCtx, fixture.dir, "v1.0.0", "")
	if !matched || tag != "v1.0.0" {
		t.Fatalf("checkout = matched %t tag %q, want v1.0.0", matched, tag)
	}
	if got := fixture.read(t); got != "v1.0.0" {
		t.Fatalf("after checkout marker = %q, want v1.0.0", got)
	}
	cancel()
	restore()
	if got := fixture.read(t); got != "head" {
		t.Fatalf("after restore marker = %q, want head", got)
	}
}

func TestCheckoutVersionTogglesVPrefix(t *testing.T) {
	fixture := newCheckoutFixture(t)
	ctx := context.Background()
	restore, tag, matched := checkoutVersion(ctx, fixture.dir, "v1.1.0", "")
	if !matched || tag != "1.1.0" || fixture.read(t) != "v1.1.0" {
		t.Fatalf("v-prefixed checkout = matched %t tag %q marker %q", matched, tag, fixture.read(t))
	}
	restore()
	restore, tag, matched = checkoutVersion(ctx, fixture.dir, "1.0.0", "")
	if !matched || tag != "v1.0.0" || fixture.read(t) != "v1.0.0" {
		t.Fatalf("plain checkout = matched %t tag %q marker %q", matched, tag, fixture.read(t))
	}
	restore()
}

func TestCheckoutVersionRejectsRevisionLikeInputs(t *testing.T) {
	fixture := newCheckoutFixture(t)
	for _, version := range []string{"--ignore-skip-worktree-bits", "v1.1.0~1"} {
		fixture.git(t, "checkout", "-q", "-f", "-B", "safe-head")
		restore, _, _ := checkoutVersion(context.Background(), fixture.dir, version, hyrum.EcoNPM)
		restore()
		if got := fixture.branch(t); got != "safe-head" {
			t.Fatalf("branch after version %q = %q, want safe-head", version, got)
		}
	}
}

func TestCheckoutVersionUnmatchedTagIsNoOp(t *testing.T) {
	fixture := newCheckoutFixture(t)
	restore, tag, matched := checkoutVersion(context.Background(), fixture.dir, "9.9.9", "")
	if matched || tag != "" {
		t.Fatalf("unmatched checkout = matched %t tag %q", matched, tag)
	}
	if got := fixture.read(t); got != "head" {
		t.Fatalf("unmatched checkout moved working tree: marker = %q", got)
	}
	restore()
	if got := fixture.read(t); got != "head" {
		t.Fatalf("no-op restore moved working tree: marker = %q", got)
	}
}

func TestCheckoutVersionEmptyVersionIsNoOp(t *testing.T) {
	fixture := newCheckoutFixture(t)
	restore, _, matched := checkoutVersion(context.Background(), fixture.dir, "", "")
	restore()
	if matched {
		t.Fatal("empty version reported a matching tag")
	}
	if got := fixture.read(t); got != "head" {
		t.Fatalf("empty-version checkout moved working tree: marker = %q", got)
	}
}

func TestCheckoutVersionNonGitDirectoryIsNoOp(t *testing.T) {
	restore, _, matched := checkoutVersion(context.Background(), t.TempDir(), "v1.0.0", "")
	restore()
	if matched {
		t.Fatal("non-git directory reported a matching tag")
	}
}

func TestPipelinePropagatesModelsToEverySkill(t *testing.T) {
	models := map[string]string{
		"hyrum-usage":    "usage-model",
		"hyrum-history":  "history-model",
		"hyrum-generate": "generate-model",
		"hyrum-validate": "validate-model",
	}
	p := &pipeline{models: models}
	runner := &recordingRunner{}

	if _, _, _, err := p.runGenerateSkills(context.Background(), runner, t.TempDir(), "ws"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := p.runValidate(context.Background(), runner, t.TempDir(), []hyrum.VerifyResult{{Pass: 1}}); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 4 {
		t.Fatalf("calls = %+v", runner.calls)
	}
	for _, call := range runner.calls {
		if call.model != models[call.name] {
			t.Errorf("%s model = %q, want %q", call.name, call.model, models[call.name])
		}
		if call.maxTurns <= harness.DefaultMaxTurns {
			t.Errorf("%s maxTurns = %d; every skill should set an explicit cap above the harness default (%d)", call.name, call.maxTurns, harness.DefaultMaxTurns)
		}
	}
}

func TestConfiguredBaselineReachesMetadata(t *testing.T) {
	target := &hyrum.Target{
		Path:   t.TempDir(),
		Report: &brief.Report{},
		Deps:   []hyrum.Dep{{Name: "ws", PURL: "pkg:npm/ws", Version: "7.4.2", Ecosystem: "npm", Direct: true}},
	}
	baseline := "8.17.1"
	selected := selectGenDeps(target, nil, map[string]hyrumconfig.Dependency{
		"ws": {Baseline: &baseline},
	})
	if len(selected) != 1 {
		t.Fatalf("selected = %+v", selected)
	}
	meta := generationMeta("target", selected[0], stagedDependency{Baseline: baseline}, &hyrum.RunResult{}, hyrum.GenerateResult{}, 0)
	if got := meta["baseline"]; got != baseline {
		t.Fatalf("baseline = %v, want %q", got, baseline)
	}
	if got := meta["constraint"]; got != baseline {
		t.Fatalf("constraint = %v, want %q", got, baseline)
	}
	if target.Deps[0].Version != "7.4.2" {
		t.Fatalf("target dependency mutated: %+v", target.Deps[0])
	}
}

func TestGenerationMetaPreservesBaselineEvidenceGaps(t *testing.T) {
	staged := stagedDependency{
		Baseline:            "8.10.0",
		OutlineRef:          "v8.10.0",
		OutlineBudgetBytes:  262144,
		OutlineBytes:        71234,
		OutlineFiles:        4,
		OutlineOmittedFiles: 500,
	}
	meta := generationMeta(
		"target",
		hyrum.Dep{PURL: "pkg:pypi/elasticsearch", Ecosystem: hyrum.EcoPyPI, Version: ">=8.10,<10"},
		staged,
		&hyrum.RunResult{},
		hyrum.GenerateResult{},
		0,
	)
	if meta["constraint"] != ">=8.10,<10" || meta["baseline"] != "8.10.0" {
		t.Fatalf("version metadata = %+v", meta)
	}
	if meta["outline_ref"] != staged.OutlineRef {
		t.Fatalf("evidence metadata = %+v", meta)
	}
	if meta["outline_budget_bytes"] != staged.OutlineBudgetBytes || meta["outline_bytes"] != staged.OutlineBytes || meta["outline_files"] != staged.OutlineFiles || meta["outline_omitted_files"] != staged.OutlineOmittedFiles {
		t.Fatalf("outline metadata = %+v", meta)
	}
	unresolved := stagedDependency{BaselineError: "registry versions unavailable", OutlineError: "source tag unavailable"}
	meta = generationMeta("target", hyrum.Dep{}, unresolved, &hyrum.RunResult{}, hyrum.GenerateResult{}, 0)
	if meta["baseline_error"] != unresolved.BaselineError || meta["outline_error"] != unresolved.OutlineError {
		t.Fatalf("unresolved metadata = %+v", meta)
	}
}

func TestRunVerifyRequiresResolvedBaseline(t *testing.T) {
	results := (&pipeline{}).runVerify(context.Background(), t.TempDir(), hyrum.Dep{}, "", "9.5.0", nil)
	if len(results) != 1 || results[0].Error != "baseline version is unresolved" {
		t.Fatalf("verify results = %+v", results)
	}
}
