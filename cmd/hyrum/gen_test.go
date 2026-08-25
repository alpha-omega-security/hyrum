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
	cfg := hyrumconfig.File{Backend: &backend, Out: &configOut, Work: &configWork}

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
		set := map[string]bool{"backend": true, "out": true, "work": true}
		got, err := resolveGenOptions(target, configPath, false, cfg, cli, set)
		if err != nil {
			t.Fatal(err)
		}
		if got.backend != "claude" || got.out != filepath.Join(target, "cli/out") || got.work != "cli/work" {
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

func TestCheckoutVersion(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	marker := filepath.Join(dir, "api.txt")
	write := func(s string) { _ = os.WriteFile(marker, []byte(s), 0o644) }
	read := func() string { b, _ := os.ReadFile(marker); return string(b) }

	git("init", "-q")
	write("v1.0.0")
	git("add", ".")
	git("commit", "-q", "-m", "one")
	git("tag", "v1.0.0")
	write("v1.1.0")
	git("commit", "-q", "-am", "two")
	git("tag", "1.1.0")
	write("head")
	git("commit", "-q", "-am", "three")

	ctx := context.Background()

	t.Run("exact tag then restore", func(t *testing.T) {
		checkoutCtx, cancel := context.WithCancel(ctx)
		restore := checkoutVersion(checkoutCtx, dir, "v1.0.0", "")
		if read() != "v1.0.0" {
			t.Fatalf("after checkout v1.0.0 marker = %q, want %q", read(), "v1.0.0")
		}
		cancel()
		restore()
		if read() != "head" {
			t.Fatalf("after restore marker = %q, want %q", read(), "head")
		}
	})

	t.Run("toggled v-prefix", func(t *testing.T) {
		// version has a leading v, tag does not.
		restore := checkoutVersion(ctx, dir, "v1.1.0", "")
		if read() != "v1.1.0" {
			t.Fatalf("after checkout v1.1.0 marker = %q; expected tag %q to match", read(), "1.1.0")
		}
		restore()
		// version has no leading v, tag does.
		restore = checkoutVersion(ctx, dir, "1.0.0", "")
		if read() != "v1.0.0" {
			t.Fatalf("after checkout 1.0.0 marker = %q; expected tag %q to match", read(), "v1.0.0")
		}
		restore()
	})

	t.Run("manifest constraint", func(t *testing.T) {
		restore := checkoutVersion(ctx, dir, "^1.0", hyrum.EcoNPM)
		if read() != "v1.0.0" {
			t.Fatalf("after checkout ^1.0 marker = %q, want %q", read(), "v1.0.0")
		}
		restore()
	})

	for _, version := range []string{"--ignore-skip-worktree-bits", "v1.1.0~1"} {
		t.Run("untrusted version "+version, func(t *testing.T) {
			git("checkout", "-q", "-f", "-B", "safe-head")
			checkoutVersion(ctx, dir, version, hyrum.EcoNPM)()
			cmd := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "HEAD")
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("version %q detached HEAD: %v", version, err)
			}
			if got := strings.TrimSpace(string(out)); got != "safe-head" {
				t.Fatalf("branch after version %q = %q, want safe-head", version, got)
			}
		})
	}

	t.Run("unmatched tag is a no-op", func(t *testing.T) {
		restore := checkoutVersion(ctx, dir, "9.9.9", "")
		if read() != "head" {
			t.Fatalf("unmatched checkout moved working tree: marker = %q", read())
		}
		restore()
		if read() != "head" {
			t.Fatalf("no-op restore moved working tree: marker = %q", read())
		}
	})

	t.Run("empty version is a no-op", func(t *testing.T) {
		checkoutVersion(ctx, dir, "", "")()
		if read() != "head" {
			t.Fatalf("empty-version checkout moved working tree: marker = %q", read())
		}
	})

	t.Run("non-git dir is a no-op", func(t *testing.T) {
		checkoutVersion(ctx, t.TempDir(), "v1.0.0", "")()
	})
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
	meta := generationMeta("target", selected[0], &hyrum.RunResult{}, hyrum.GenerateResult{}, 0)
	if got := meta["baseline"]; got != baseline {
		t.Fatalf("baseline = %v, want %q", got, baseline)
	}
	if target.Deps[0].Version != "7.4.2" {
		t.Fatalf("target dependency mutated: %+v", target.Deps[0])
	}
}
