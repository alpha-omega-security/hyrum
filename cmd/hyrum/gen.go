package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alpha-omega-security/harness"
	harnesscontainer "github.com/alpha-omega-security/harness/container"
	hyrumconfig "github.com/alpha-omega-security/hyrum/internal/config"
	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/alpha-omega-security/hyrum/internal/hyrum/usage"
	"github.com/git-pkgs/clone"
	"github.com/git-pkgs/outline"
	"github.com/git-pkgs/registries"
	_ "github.com/git-pkgs/registries/all" // register every ecosystem's init()
	"github.com/git-pkgs/vers"
)

// skillSteps are the harness invocations genOne runs in order. usage and
// history are best-effort (their outputs feed generate but generate can
// still work from usage.json and dep-outline.md alone); a generate failure
// is fatal because there is nothing to write without it. maxTurns overrides
// harness.DefaultMaxTurns per skill; the values reflect observed turn counts
// on small targets with headroom for larger dependencies and changelogs.
type skillStep struct {
	name, out string
	required  bool
	maxTurns  int
}

var skillSteps = []skillStep{
	{"hyrum-usage", "surface.json", false, 40},
	{"hyrum-history", "breaks.json", false, 50},
	{"hyrum-generate", "tests.json", true, 60},
}

const validateMaxTurns = 40

// depOutlineIgnore excludes files from dep-outline.md that carry no API
// signal for test generation: licence text, CI configuration, editor and
// linter settings. README.md is kept because usage examples there sometimes
// document behaviour the source does not.
var depOutlineIgnore = []string{
	"LICENSE*", "LICENCE*", "COPYING*", "NOTICE*",
	".github/", ".gitlab-ci.yml", ".circleci/", ".travis.yml",
	".golangci.*", ".editorconfig", ".prettierrc*", ".eslintrc*",
	"renovate.json", ".dependabot/", "CODEOWNERS", ".gitattributes",
}

const (
	defaultGenBackend = "claude"
	defaultGenOut     = "tests/hyrum"
)

type genOptions struct {
	backend      string
	out          string
	work         string
	targetName   string
	outlineBytes int
	models       map[string]string
}

func defaultGenOptions() genOptions {
	return genOptions{
		backend:      defaultGenBackend,
		out:          defaultGenOut,
		work:         defaultWorkPath("work"),
		outlineBytes: defaultOutlineBytes,
	}
}

// cmdGen runs the generation pipeline for one or more dependencies: stage the
// context bundle, gather history inputs, then run the usage/history/generate
// skills in sequence. Without --run it stops after staging so the workspace
// can be inspected without invoking a backend.
func cmdGen(ctx context.Context, args []string) error {
	fs := newFlags("gen")
	defaults := defaultGenOptions()
	var deps, symbols, scopes, includes, excludes stringList
	fs.Var(&deps, "dep", "dependency name to generate for (repeatable); default: all direct runtime deps")
	fs.Var(&symbols, "symbol", "exact dependency symbol to include (repeatable; requires one --dep)")
	fs.Var(&scopes, "scope", "usage scope to include: production, test, example, or documentation (repeatable); default: production")
	fs.Var(&includes, "include", "relative path prefix to include when indexing usage (repeatable)")
	fs.Var(&excludes, "exclude", "relative path prefix to exclude when indexing usage (repeatable)")
	configPath := fs.String("config", "", "configuration file (default: <target>/hyrum.yaml when present)")
	out := fs.String("out", defaults.out, "output root for generated tests")
	work := fs.String("work", defaults.work, "working directory for clones and skill workspaces")
	backend := fs.String("backend", defaults.backend, "harness backend: "+harness.Names())
	run := fs.Bool("run", false, "actually invoke the backend (otherwise stage only)")
	batchSize := fs.Int("batch-size", 0, "maximum symbol entries per model batch; zero disables this limit")
	batchSites := fs.Int("batch-sites", 0, "maximum static sites per model batch; zero disables this limit")
	outlineBytes := fs.Int("outline-bytes", defaults.outlineBytes, "maximum bytes in each dependency outline")
	targetNameOverride := fs.String("target-name", "", "stable name used in work and output paths (default: package or repository identity)")
	container := fs.String("container", "", "run the backend in a container using this image (\"default\" for "+hyrum.DefaultRunnerImage+")")
	verify := fs.Bool("verify", false, "after generating, run the tests against the baseline and latest dep versions and record results in meta.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("unexpected arguments after <path>: %v (flags must precede <path>)", fs.Args()[1:])
	}
	if len(symbols) > 0 && len(deps) != 1 {
		return fmt.Errorf("--symbol requires exactly one --dep")
	}
	if *batchSize < 0 {
		return fmt.Errorf("--batch-size must be zero or greater")
	}
	if *batchSites < 0 {
		return fmt.Errorf("--batch-sites must be zero or greater")
	}
	if *outlineBytes <= 0 {
		return fmt.Errorf("--outline-bytes must be greater than zero")
	}
	set := visitedFlags(fs)
	if set["target-name"] {
		if err := validateTargetName(*targetNameOverride); err != nil {
			return err
		}
	}
	usageOptions, err := resolveUsageOptions(scopes, includes, excludes, []usage.Scope{usage.ScopeProduction})
	if err != nil {
		return err
	}
	path := "."
	if fs.NArg() > 0 {
		path = fs.Arg(0)
	}

	t, err := hyrum.Analyze(path)
	if err != nil {
		return err
	}

	if set["config"] && *configPath == "" {
		return fmt.Errorf("--config requires a non-empty path")
	}
	cfg, cfgSource, err := hyrumconfig.Load(t.Path, *configPath)
	if err != nil {
		return err
	}
	resolved, err := resolveGenOptions(t.Path, cfgSource, set["config"], cfg, genOptions{
		backend:      *backend,
		out:          *out,
		work:         *work,
		targetName:   *targetNameOverride,
		outlineBytes: *outlineBytes,
	}, set)
	if err != nil {
		return err
	}
	if !set["work"] && (!set["config"] || cfg.Work == nil) {
		if err := ensurePrivateWorkRoot(resolved.work); err != nil {
			return err
		}
	}

	selected := selectGenDeps(t, deps, cfg.Deps)
	if len(selected) == 0 {
		return fmt.Errorf("no dependencies selected (target has %d direct deps across %v)", len(t.Deps), t.Ecosystems())
	}
	usageOptions = withConfiguredActivations(usageOptions, selected, cfg.Deps)

	h, err := harness.ByName(resolved.backend)
	if err != nil {
		return err
	}
	models, err := resolveModels(h, resolved.models)
	if err != nil {
		return err
	}

	p := newPipeline(h, resolved.work, resolved.out, *run, resolveContainer(*container))
	p.models = models
	p.verify = *verify
	p.usageOptions = usageOptions
	p.symbols = append([]string(nil), symbols...)
	p.batchSize = *batchSize
	p.batchSites = *batchSites
	p.outlineBytes = resolved.outlineBytes
	p.targetName = resolved.targetName
	return p.genAll(ctx, t, selected)
}

// resolveGenOptions applies built-in defaults, then config values, then only
// the flags the user explicitly supplied. Its returned out path is rooted at
// the target. Work from an auto-discovered config is ignored; an explicitly
// selected config resolves work relative to the config directory.
func resolveGenOptions(targetRoot, configPath string, explicitConfig bool, cfg hyrumconfig.File, cli genOptions, set map[string]bool) (genOptions, error) {
	resolved := defaultGenOptions()
	resolved.out = outRoot(targetRoot, resolved.out)

	if explicitConfig && cfg.Backend != nil {
		resolved.backend = *cfg.Backend
	}
	if cfg.Out != nil {
		expanded, err := hyrumconfig.ExpandUser(*cfg.Out)
		if err != nil {
			return genOptions{}, fmt.Errorf("out: %w", err)
		}
		resolved.out = outRoot(targetRoot, expanded)
	}
	if explicitConfig && cfg.Work != nil {
		work, err := hyrumconfig.ResolvePath(configPath, *cfg.Work)
		if err != nil {
			return genOptions{}, fmt.Errorf("work: %w", err)
		}
		resolved.work = work
	}
	if cfg.OutlineBytes != nil {
		resolved.outlineBytes = *cfg.OutlineBytes
	}
	if cfg.TargetName != nil {
		resolved.targetName = *cfg.TargetName
	}
	if explicitConfig {
		resolved.models = cfg.Models
	}

	if set["backend"] {
		resolved.backend = cli.backend
	}
	if set["out"] {
		expanded, err := hyrumconfig.ExpandUser(cli.out)
		if err != nil {
			return genOptions{}, fmt.Errorf("out: %w", err)
		}
		resolved.out = outRoot(targetRoot, expanded)
	}
	if set["work"] {
		expanded, err := hyrumconfig.ExpandUser(cli.work)
		if err != nil {
			return genOptions{}, fmt.Errorf("work: %w", err)
		}
		resolved.work = expanded
	}
	if set["outline-bytes"] {
		resolved.outlineBytes = cli.outlineBytes
	}
	if set["target-name"] {
		resolved.targetName = cli.targetName
	}
	if resolved.targetName != "" {
		if err := validateTargetName(resolved.targetName); err != nil {
			return genOptions{}, err
		}
	}
	trustedExternalOut := set["out"] || (explicitConfig && cfg.Out != nil)
	if !trustedExternalOut && !pathWithinResolved(targetRoot, resolved.out) {
		return genOptions{}, fmt.Errorf("out: automatic output must resolve inside target %q (use --out or explicit --config to trust an external output path)", targetRoot)
	}
	return resolved, nil
}

func pathWithinResolved(base, path string) bool {
	resolvedBase, err := resolveExistingPath(base)
	if err != nil {
		return false
	}
	resolvedPath, err := resolveExistingPath(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(resolvedBase, resolvedPath)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveExistingPath resolves every existing symlink prefix of path, then
// appends any not-yet-created suffix. Callers use it before creating output
// paths so a symlink already present in an untrusted target cannot redirect a
// nominally contained path elsewhere on the host.
func resolveExistingPath(path string) (string, error) {
	current, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	var missing []string
	for {
		resolved, evalErr := filepath.EvalSymlinks(current)
		if evalErr == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(evalErr, os.ErrNotExist) {
			return "", evalErr
		}
		info, lstatErr := os.Lstat(current)
		if lstatErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("resolve symlink %q: %w", current, evalErr)
			}
			return "", evalErr
		}
		if !errors.Is(lstatErr, os.ErrNotExist) {
			return "", lstatErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", evalErr
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func visitedFlags(fs *flag.FlagSet) map[string]bool {
	set := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		set[f.Name] = true
	})
	return set
}

func resolveModels(h harness.Harness, tiers map[string]string) (map[string]string, error) {
	models := make(map[string]string, len(tiers))
	for skill, tier := range tiers {
		for _, candidate := range h.DefaultModels() {
			if candidate.Tier == tier {
				models[skill] = candidate.ID
				break
			}
		}
		if models[skill] == "" {
			return nil, fmt.Errorf("models.%s: backend %s has no %q model tier", skill, harness.Name(h), tier)
		}
	}
	return models, nil
}

// pipeline holds the shared configuration for running the stage → history →
// skills → write sequence over one or more dependencies of one target.
// cmdGen and cmdCorpus both drive it.
type pipeline struct {
	h            harness.Harness
	rc           *registries.Client
	runner       hyrum.Runner
	work         string
	outRoot      string
	run          bool
	usageOptions usage.IndexOptions
	// symbols limits generation to exact static symbol names.
	symbols []string
	// batchSize is the maximum number of symbols sent through model steps in
	// one batch. Zero disables the symbol limit.
	batchSize int
	// batchSites limits static sites per batch and can split one symbol's sites
	// across batches. Zero disables the site limit.
	batchSites int
	// outlineBytes caps the rendered dependency outline passed to model steps.
	outlineBytes int
	// targetName overrides the identity detected from the target manifest or
	// repository path.
	targetName string
	// models maps skill names to backend-owned model IDs resolved from the
	// portable mid/high/max tiers in hyrum.yaml.
	models map[string]string
	// containerImage non-empty means the runner is a ContainerRunner and each
	// genOne call sets its TargetPath to the analysed target so /work/target
	// is a read-only bind mount instead of a host symlink.
	containerImage   string
	containerRuntime *harnesscontainer.Runtime
	// verify runs the generated tests against baseline and latest after
	// writing them and records results in meta.json.
	verify bool
}

type generationExecution struct {
	Result         *hyrum.RunResult
	Generate       hyrum.GenerateResult
	TotalCost      float64
	RecoveredSteps []string
	Batch          *batchedGeneration
}

type stagedDependency struct {
	DepDir              string
	Latest              string
	Baseline            string
	BaselineError       string
	OutlineRef          string
	OutlineError        string
	OutlineBudgetBytes  int
	OutlineBytes        int
	OutlineFiles        int
	OutlineOmittedFiles int
}

type registryDependency struct {
	Repository    string
	Latest        string
	Baseline      string
	BaselineError string
}

// newPipeline builds a pipeline from the flags gen and corpus share.
func newPipeline(h harness.Harness, work, outRoot string, run bool, containerImage string) *pipeline {
	p := &pipeline{
		h:              h,
		rc:             registries.DefaultClient(),
		work:           work,
		outRoot:        outRoot,
		run:            run,
		usageOptions:   usage.IndexOptions{Scopes: []usage.Scope{usage.ScopeProduction}},
		outlineBytes:   defaultOutlineBytes,
		containerImage: containerImage,
	}
	if containerImage == "" {
		p.runner = hyrum.HostRunner{}
	}
	// ContainerRunner is constructed per genOne with the target path.
	return p
}

// genAll builds the target's history index once and runs genOne for each dep.
func (p *pipeline) genAll(ctx context.Context, t *hyrum.Target, deps []hyrum.Dep) error {
	idx, err := hyrum.BuildHistoryIndex(ctx, t, deps)
	if err != nil {
		return fmt.Errorf("history index: %w", err)
	}
	var genErrs []error
	for _, d := range deps {
		if err := p.genOne(ctx, t, idx, d); err != nil {
			depErr := fmt.Errorf("%s: %w", d.Name, err)
			fmt.Fprintf(os.Stderr, "  %s\n", safeLine(depErr.Error()))
			genErrs = append(genErrs, depErr)
		}
	}
	return errors.Join(genErrs...)
}

func (p *pipeline) genOne(ctx context.Context, t *hyrum.Target, idx *hyrum.HistoryIndex, d hyrum.Dep) error {
	targetDir := targetName(t)
	if p.targetName != "" {
		targetDir = p.targetName
	}
	if err := validateTargetName(targetDir); err != nil {
		return err
	}
	if err := validateRelativePath("ecosystem", d.Ecosystem); err != nil {
		return err
	}
	if err := validateRelativePath("dependency name", d.Name); err != nil {
		return err
	}
	wsRel := filepath.Join(targetDir, d.Ecosystem, d.Name)
	ws := filepath.Join(p.work, wsRel)
	if !pathWithinResolved(p.work, ws) {
		return fmt.Errorf("dependency %q resolves outside work root %q", d.Name, p.work)
	}
	outRel := filepath.Join(d.Name, "from_"+targetDir)
	outDir := filepath.Join(p.outRoot, outRel)
	if !pathWithinResolved(p.outRoot, outDir) {
		return fmt.Errorf("dependency %q resolves outside output root %q", d.Name, p.outRoot)
	}
	preparedWS, err := prepareWorkspaceUnder(p.work, wsRel)
	if err != nil {
		return fmt.Errorf("prepare workspace: %w", err)
	}
	ws = preparedWS
	depCheckout, err := prepareExternalCheckout(p.work, filepath.Join(".deps", d.Ecosystem, d.Name))
	if err != nil {
		return fmt.Errorf("prepare dependency checkout: %w", err)
	}
	staged, err := stageContext(ctx, t, targetDir, d, ws, depCheckout, p.rc, p.usageOptions, p.symbols, p.outlineBytes)
	if err != nil {
		return fmt.Errorf("stage: %w", err)
	}
	var batches []*usage.Surface
	if p.batchingEnabled() {
		surface, err := readUsageSurface(ws, "usage.json")
		if err != nil {
			return fmt.Errorf("read staged usage: %w", err)
		}
		batches = usage.PartitionSurface(surface, p.batchSize, p.batchSites)
		if err := stageUsageBatches(ws, batches); err != nil {
			return fmt.Errorf("stage usage batches: %w", err)
		}
	}
	historyDep := d
	historyDep.Version = staged.Baseline
	if err := hyrum.GatherHistory(ctx, idx, historyDep, staged.DepDir, staged.Latest, ws); err != nil {
		fmt.Fprintf(os.Stderr, "  %s: history: %s\n", safeLine(d.Name), safeLine(err.Error()))
	}
	if !p.run {
		if p.batchingEnabled() {
			fmt.Printf("staged %s: %d batch(es) under %s; --run executes and merges them\n", safeLine(d.Name), len(batches), safeLine(filepath.Join(ws, "batches")))
			return nil
		}
		last := skillSteps[len(skillSteps)-1]
		job := harness.Job{
			Workspace: ws, SrcDir: hyrum.TargetSubdir, SkillName: last.name,
			OutputFile: last.out, Model: p.models[last.name], MaxTurns: last.maxTurns,
		}
		fmt.Printf("staged %s: %s %s\n", safeLine(d.Name), safeLine(p.h.Binary()), safeLine(fmt.Sprint(p.h.Args(job))))
		return nil
	}

	fmt.Fprintf(os.Stderr, "→ %s ← %s (%s)\n", safeLine(d.Name), safeLine(targetDir), safeLine(d.PURL))
	runner, err := p.runnerFor(t.Path, staged.DepDir)
	if err != nil {
		return err
	}
	execution, err := p.runSelectedGeneration(ctx, runner, ws, d.Name, batches)
	if err != nil {
		return err
	}
	res := execution.Result
	gen := execution.Generate
	totalCost := execution.TotalCost
	recoveredSteps := execution.RecoveredSteps

	written, err := hyrum.ReplaceFilesUnder(p.outRoot, outRel, gen.Files)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}
	meta := generationMeta(targetDir, d, staged, res, gen, totalCost)
	if execution.Batch != nil {
		addBatchMetadata(meta, p.batchSize, p.batchSites, execution.Batch)
	}
	if p.verify {
		verify := p.runVerify(ctx, ws, d, staged.Baseline, staged.Latest, gen.Files)
		meta["verify"] = verify
		v, cost, recovery, err := p.runValidate(ctx, runner, ws, verify)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  validate: %s\n", safeLine(err.Error()))
		} else if v != nil {
			if recovery != "" {
				recoveredSteps = append(recoveredSteps, "hyrum-validate")
			}
			meta["validate"] = v.Verdicts
			totalCost += cost
			meta["cost_usd"] = totalCost
			reportVerdicts(v.Verdicts)
		}
	}
	addBackendRecoveries(meta, recoveredSteps)
	if err := writeJSONUnder(p.outRoot, outRel, "meta.json", meta); err != nil {
		return err
	}
	fmt.Printf("%s ← %s: %d file(s) → %s ($%.4f)\n", safeLine(d.Name), safeLine(targetDir), len(written), safeLine(outDir), totalCost)
	return nil
}

func (p *pipeline) runnerFor(targetPath, dependencyPath string) (hyrum.Runner, error) { //nolint:ireturn // Selects a Runner implementation.
	if p.runner != nil {
		return p.runner, nil
	}
	if p.containerRuntime == nil {
		runtime, err := hyrum.DetectContainerRuntime("")
		if err != nil {
			return nil, err
		}
		p.containerRuntime = &runtime
	}
	return hyrum.ContainerRunner{
		Runtime: *p.containerRuntime, Image: p.containerImage,
		TargetPath: targetPath, DependencyPath: dependencyPath,
	}, nil
}

func (p *pipeline) runSelectedGeneration(
	ctx context.Context,
	runner hyrum.Runner,
	ws, depName string,
	batches []*usage.Surface,
) (*generationExecution, error) {
	if p.batchingEnabled() {
		run, err := p.runBatchedGenerateSkills(ctx, runner, ws, depName, batches)
		if err != nil {
			return nil, err
		}
		return &generationExecution{
			Result: run.LastResult, Generate: run.Generate, TotalCost: run.TotalCost,
			RecoveredSteps: run.RecoveredSteps, Batch: run,
		}, nil
	}
	result, cost, recovered, err := p.runGenerateSkills(ctx, runner, ws, depName)
	if err != nil {
		return nil, err
	}
	var generated hyrum.GenerateResult
	if err := result.Decode(&generated); err != nil {
		return nil, fmt.Errorf("decode tests.json: %w", err)
	}
	return &generationExecution{
		Result: result, Generate: generated, TotalCost: cost, RecoveredSteps: recovered,
	}, nil
}

func (p *pipeline) batchingEnabled() bool {
	return p.batchSize > 0 || p.batchSites > 0
}

var transientWorkspaceFiles = []string{
	"dep-outline.md",
	outlineSelectionFile,
	"git-log.txt",
	"changelog.json",
	"vulns.json",
	"surface.json",
	"breaks.json",
	"tests.json",
	"verify.json",
	"verdict.json",
	"schema.json",
}

var transientWorkspaceDirs = []string{"batches", "dep", "verify"}

func prepareWorkspace(ws string) error {
	_, err := prepareWorkspaceUnder(filepath.Dir(ws), filepath.Base(ws))
	return err
}

func addBackendRecoveries(meta map[string]any, steps []string) {
	if len(steps) == 0 {
		return
	}
	meta["recovered_output"] = true
	meta["recovered_steps"] = steps
}

func (p *pipeline) runGenerateSkills(ctx context.Context, runner hyrum.Runner, ws, depName string) (*hyrum.RunResult, float64, []string, error) {
	var res *hyrum.RunResult
	var totalCost float64
	var recoveredSteps []string
	for _, s := range skillSteps {
		r, err := p.runGenerationStep(ctx, runner, ws, depName, s)
		if err != nil {
			if s.required {
				return nil, totalCost, recoveredSteps, err
			}
			continue
		}
		if r.BackendError != "" {
			recoveredSteps = append(recoveredSteps, s.name)
		}
		totalCost += r.CostUSD
		res = r
	}
	if res == nil {
		return nil, totalCost, recoveredSteps, fmt.Errorf("generate produced no output")
	}
	return res, totalCost, recoveredSteps, nil
}

func (p *pipeline) runGenerationStep(
	ctx context.Context,
	runner hyrum.Runner,
	ws, depName string,
	step skillStep,
) (*hyrum.RunResult, error) {
	fmt.Fprintf(os.Stderr, "  [%s]\n", step.name)
	result, err := runner.RunSkill(ctx, p.h, ws, step.name, step.out, hyrum.RunOptions{
		Model: p.models[step.name], MaxTurns: step.maxTurns,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %s/%s: %s\n", safeLine(depName), safeLine(step.name), safeLine(err.Error()))
		return nil, err
	}
	if result.BackendError != "" {
		fmt.Fprintf(os.Stderr, "  ! %s/%s: %s\n", safeLine(depName), safeLine(step.name), safeLine(result.BackendError))
	}
	return result, nil
}

func generationMeta(targetDir string, d hyrum.Dep, staged stagedDependency, res *hyrum.RunResult, gen hyrum.GenerateResult, totalCost float64) map[string]any {
	meta := map[string]any{
		"purl":       d.PURL,
		"ecosystem":  d.Ecosystem,
		"constraint": d.Version,
		"baseline":   staged.Baseline,
		"target":     targetDir,
		"session_id": res.SessionID,
		"cost_usd":   totalCost,
		"notes":      gen.Notes,
	}
	if staged.BaselineError != "" {
		meta["baseline_error"] = staged.BaselineError
	}
	if staged.OutlineError != "" {
		meta["outline_error"] = staged.OutlineError
	}
	if staged.OutlineRef != "" {
		meta["outline_ref"] = staged.OutlineRef
	}
	if staged.OutlineBudgetBytes > 0 {
		meta["outline_budget_bytes"] = staged.OutlineBudgetBytes
		meta["outline_bytes"] = staged.OutlineBytes
		meta["outline_files"] = staged.OutlineFiles
		meta["outline_omitted_files"] = staged.OutlineOmittedFiles
	}
	return meta
}

// runValidate stages the verify results and runs the hyrum-validate skill to
// classify each latest-version failure and flag weak assertions. It returns
// nil when there is nothing to validate (verify errored or ran no tests).
func (p *pipeline) runValidate(ctx context.Context, runner hyrum.Runner, ws string, verify []hyrum.VerifyResult) (*hyrum.ValidateResult, float64, string, error) {
	if !anyRan(verify) {
		return nil, 0, "", nil
	}
	if err := writeWorkspaceJSON(ws, "verify.json", verify); err != nil {
		return nil, 0, "", err
	}
	fmt.Fprintf(os.Stderr, "  [hyrum-validate]\n")
	r, err := runner.RunSkill(ctx, p.h, ws, "hyrum-validate", "verdict.json", hyrum.RunOptions{Model: p.models["hyrum-validate"], MaxTurns: validateMaxTurns})
	if err != nil {
		return nil, 0, "", err
	}
	if r.BackendError != "" {
		fmt.Fprintf(os.Stderr, "  ! hyrum-validate: %s\n", safeLine(r.BackendError))
	}
	var out hyrum.ValidateResult
	if err := r.Decode(&out); err != nil {
		return nil, r.CostUSD, "", fmt.Errorf("decode verdict.json: %w", err)
	}
	return &out, r.CostUSD, r.BackendError, nil
}

// anyRan reports whether at least one version's tests were parsed. A verify
// slice of only Error entries means the runner never got as far as executing
// tests, so there is nothing for validate to classify.
func anyRan(rs []hyrum.VerifyResult) bool {
	for _, r := range rs {
		if r.Pass > 0 || r.Fail > 0 {
			return true
		}
	}
	return false
}

func reportVerdicts(vs []hyrum.Verdict) {
	if len(vs) == 0 {
		fmt.Fprintf(os.Stderr, "    validate: all tests adequate\n")
		return
	}
	by := map[string]int{}
	for _, v := range vs {
		by[v.Status]++
		fmt.Fprintf(os.Stderr, "    %s [%s→%s]: %s\n", safeLine(v.Test), safeLine(v.Status), safeLine(v.Action), safeLine(v.Reasoning))
	}
	fmt.Fprintf(os.Stderr, "    validate: %d verdict(s) — %s\n", len(vs), safeLine(fmt.Sprint(by)))
}

// runVerify installs the dep at baseline and latest in a scratch dir under
// the workspace and runs the generated tests against each. The scratch dir is
// separate from the target so the user's checkout and lockfile are untouched.
func (p *pipeline) runVerify(ctx context.Context, ws string, d hyrum.Dep, baseline, latest string, files []hyrum.GeneratedFile) []hyrum.VerifyResult {
	if baseline == "" {
		return []hyrum.VerifyResult{{Error: "baseline version is unresolved"}}
	}
	scratch := filepath.Join(ws, "verify")
	if err := removeWorkspacePath(ws, "verify", true); err != nil {
		return []hyrum.VerifyResult{{Error: err.Error()}}
	}
	if err := makeWorkspaceDirectory(ws, "verify"); err != nil {
		return []hyrum.VerifyResult{{Error: err.Error()}}
	}
	mgr, testCommand, err := verificationRuntime(scratch, d.Ecosystem)
	if err != nil {
		return []hyrum.VerifyResult{{Error: fmt.Sprintf("verification runtime for %s: %v", d.Ecosystem, err)}}
	}
	versions := []string{baseline, latest}
	fmt.Fprintf(os.Stderr, "  [verify] %s at %s\n", safeLine(d.Name), safeLine(fmt.Sprint(versions)))
	results := hyrum.VerifyMatrix(ctx, mgr, testCommand, scratch, d.Name, files, versions)
	for _, r := range results {
		if r.Error != "" {
			fmt.Fprintf(os.Stderr, "    %s: error: %s\n", safeLine(r.Version), safeLine(r.Error))
		} else {
			fmt.Fprintf(os.Stderr, "    %s: %d pass, %d fail %s\n", safeLine(r.Version), r.Pass, r.Fail, safeLine(fmt.Sprint(r.Failed)))
		}
	}
	return results
}

// resolveContainer maps the --container flag value to an image name. Empty
// means host mode; "default" selects the published runner image.
func resolveContainer(v string) string {
	if v == "default" {
		return hyrum.DefaultRunnerImage
	}
	return v
}

// outRoot resolves the --out flag: absolute paths are used as-is; relative
// paths are joined onto the target so tests/hyrum lands next to the code that
// was analysed.
func outRoot(targetPath, out string) string {
	if filepath.IsAbs(out) {
		return out
	}
	return filepath.Join(targetPath, out)
}

// validateRelativePath rejects values that would escape a configured root
// when passed to filepath.Join. Some values may contain legitimate path
// separators, including scoped npm packages, Go modules, and Composer
// packages, so keep those layouts while rejecting absolute, empty,
// traversing, and unclean paths.
func validateRelativePath(label, value string) error {
	if !filepath.IsLocal(value) || value == "." || filepath.Clean(value) != value {
		return fmt.Errorf("unsafe %s %q", label, value)
	}
	return nil
}

func validateTargetName(value string) error {
	if err := validateRelativePath("target name", value); err != nil {
		return err
	}
	if filepath.Base(value) != value || strings.ContainsAny(value, `/\\`) {
		return fmt.Errorf("unsafe target name %q: must be one path component", value)
	}
	return nil
}

// targetName is the from_<x> directory component. Prefers the basename of the
// origin remote, then any https remote, then the path basename. brief's
// Remotes is a map so plain iteration would pick a deploy remote (dokku,
// heroku) at random.
func targetName(t *hyrum.Target) string {
	if t.Name != "" {
		return t.Name
	}
	if t.Report != nil && t.Report.Git != nil {
		remotes := t.Report.Git.Remotes
		if u := remotes["origin"]; u != "" {
			return remoteBasename(u)
		}
		for _, u := range remotes {
			if strings.HasPrefix(u, "https://") {
				return remoteBasename(u)
			}
		}
	}
	return filepath.Base(t.Path)
}

func remoteBasename(url string) string {
	base := filepath.Base(url)
	if i := len(base) - len(".git"); i > 0 && base[i:] == ".git" {
		base = base[:i]
	}
	return base
}

// stageContext writes the per-dependency context bundle into ws:
//
//	ws/target/            symlink or copy of the target checkout
//	ws/dep/               full clone of the dependency's source repo
//	ws/dep-outline.md     outline.Pack of the resolved baseline tag
//	ws/usage.json         scoped static usage of target against dep
//	ws/context.json       purl, versions, ecosystem
//
// The returned staging result carries the dep clone directory, resolved
// baseline, latest registry version, and any evidence gaps that later metadata
// and verification must preserve.
func stageContext(
	ctx context.Context,
	t *hyrum.Target,
	target string,
	d hyrum.Dep,
	ws string,
	depCheckout string,
	rc *registries.Client,
	usageOptions usage.IndexOptions,
	symbols []string,
	outlineBytes int,
) (staged stagedDependency, err error) {
	staged.OutlineBudgetBytes = outlineBytes
	// Target: symlink so the skill sees the real tree without a copy.
	if err := linkWorkspaceDirectory(ws, hyrum.TargetSubdir, t.Path); err != nil {
		return staged, fmt.Errorf("link target: %w", err)
	}

	// Usage surface (works even without the dep clone).
	surf, err := usage.IndexWithOptions(ctx, t.Path, d.PURL, usageOptions)
	if err != nil {
		return staged, fmt.Errorf("usage: %w", err)
	}
	surf, err = selectUsageSymbols(surf, symbols)
	if err != nil {
		return staged, fmt.Errorf("usage: %w", err)
	}
	if err := writeWorkspaceJSON(ws, "usage.json", surf); err != nil {
		return staged, err
	}

	// Dep source + outline: best-effort. Registry metadata supplies the repo
	// URL; clone.Ensure maintains a full checkout; outline.Pack reduces it.
	registryInfo, rerr := lookupDependency(ctx, rc, d)
	staged.Latest = registryInfo.Latest
	staged.Baseline = registryInfo.Baseline
	staged.BaselineError = registryInfo.BaselineError
	if rerr != nil {
		staged.BaselineError = fmt.Sprintf("registry metadata: %v", rerr)
	}
	meta, repositoryErr := dependencyMetadata(d, target, outlineBytes, registryInfo, rerr, &staged)
	if err := writeWorkspaceJSON(ws, "context.json", meta); err != nil {
		return staged, err
	}
	if registryInfo.Repository == "" {
		return staged, nil
	}
	if err := stageDependencySource(ctx, d, ws, depCheckout, registryInfo.Repository, repositoryErr, surf, outlineBytes, &staged, meta); err != nil {
		return staged, err
	}
	return staged, nil
}

func dependencyMetadata(
	d hyrum.Dep,
	target string,
	outlineBytes int,
	registryInfo registryDependency,
	registryErr error,
	staged *stagedDependency,
) (map[string]any, error) {
	repositoryDisplay := ""
	var repositoryErr error
	if registryInfo.Repository != "" {
		repositoryErr = validateRepositoryURL(registryInfo.Repository)
		if repositoryErr == nil {
			repositoryDisplay = clone.RedactURL(registryInfo.Repository)
		} else {
			repositoryDisplay = "[invalid repository URL]"
		}
	}
	meta := map[string]any{
		"purl":                 d.PURL,
		"name":                 d.Name,
		"ecosystem":            d.Ecosystem,
		"constraint":           d.Version,
		"version":              staged.Baseline,
		"baseline":             staged.Baseline,
		"repo":                 repositoryDisplay,
		"latest":               staged.Latest,
		"target":               target,
		"outline_budget_bytes": outlineBytes,
	}
	if registryErr != nil {
		meta["registry_error"] = registryErr.Error()
	}
	if staged.BaselineError != "" {
		meta["baseline_error"] = staged.BaselineError
		fmt.Fprintf(os.Stderr, "  %s: baseline: %s\n", safeLine(d.Name), safeLine(staged.BaselineError))
	}
	if registryInfo.Repository == "" {
		staged.OutlineError = "dependency repository URL is unavailable"
		meta["outline_error"] = staged.OutlineError
		fmt.Fprintf(os.Stderr, "  %s: outline unavailable: %s\n", safeLine(d.Name), safeLine(staged.OutlineError))
	}
	return meta, repositoryErr
}

func stageDependencySource(
	ctx context.Context,
	d hyrum.Dep,
	ws, depCheckout, repository string,
	repositoryErr error,
	surf *usage.Surface,
	outlineBytes int,
	staged *stagedDependency,
	meta map[string]any,
) error {
	if repositoryErr != nil {
		staged.OutlineError = repositoryErr.Error()
		meta["outline_error"] = staged.OutlineError
		if writeErr := writeWorkspaceJSON(ws, "context.json", meta); writeErr != nil {
			return writeErr
		}
		return nil
	}
	staged.DepDir = depCheckout
	// Full clone: hyrum-history diffs between version tags and reads
	// History.md at old refs, which a shallow clone cannot serve. The dep
	// clone is reused across runs via Ensure so the cost is one-time.
	// The dep clone and everything derived from it is best-effort: a bad
	// repository URL from the registry (or none) means the skills run with
	// usage.json and git-log.txt only.
	if err := clone.Ensure(ctx, clone.Retry{}, repository, staged.DepDir, "", true); err != nil {
		fmt.Fprintf(os.Stderr, "  clone %s: %s (continuing without dep source)\n", safeLine(clone.RedactURL(repository)), safeLine(err.Error()))
		staged.OutlineError = fmt.Sprintf("clone dependency source: %v", err)
		meta["outline_error"] = staged.OutlineError
		staged.DepDir = ""
		if writeErr := writeWorkspaceJSON(ws, "context.json", meta); writeErr != nil {
			return writeErr
		}
		return nil
	}
	if err := linkWorkspaceDirectory(ws, "dep", staged.DepDir); err != nil {
		return fmt.Errorf("link dependency source: %w", err)
	}
	return stageDependencyOutline(ctx, d, ws, surf, outlineBytes, staged, meta)
}

func stageDependencyOutline(
	ctx context.Context,
	d hyrum.Dep,
	ws string,
	surf *usage.Surface,
	outlineBytes int,
	staged *stagedDependency,
	meta map[string]any,
) error {
	// Outline the dependency at the resolved baseline release so
	// generated tests reference the baseline API rather than symbols added or
	// changed since. The working tree is restored afterwards so writeChangelog
	// (in GatherHistory) still reads the up-to-date changelog. Both trees are
	// stripped of instruction files because the skill later reads dep/ directly.
	restore, tag, matched := checkoutVersion(ctx, staged.DepDir, staged.Baseline, d.Ecosystem)
	if !matched {
		if _, err := harness.StripDirectives(staged.DepDir); err != nil {
			return fmt.Errorf("strip %s: %w", staged.DepDir, err)
		}
		if staged.Baseline != "" {
			staged.OutlineError = fmt.Sprintf("no source tag matched resolved baseline %s", staged.Baseline)
			fmt.Fprintf(os.Stderr, "  %s: outline unavailable: %s\n", safeLine(d.Name), safeLine(staged.OutlineError))
		} else {
			staged.OutlineError = "dependency outline unavailable because the baseline version is unresolved"
		}
		meta["outline_error"] = staged.OutlineError
		if err := writeWorkspaceJSON(ws, "context.json", meta); err != nil {
			return err
		}
		return nil
	}
	staged.OutlineRef = tag
	meta["outline_ref"] = tag
	if _, err := harness.StripDirectives(staged.DepDir); err != nil {
		restore()
		return fmt.Errorf("strip %s: %w", staged.DepDir, err)
	}
	res, err := outline.Pack(staged.DepDir, outline.Options{Compress: true, Ignore: depOutlineIgnore})
	restore()
	if _, serr := harness.StripDirectives(staged.DepDir); serr != nil {
		return fmt.Errorf("strip %s: %w", staged.DepDir, serr)
	}
	if err != nil {
		return fmt.Errorf("outline: %w", err)
	}
	selection, err := selectDependencyOutline(res, surf, outlineBytes)
	if err != nil {
		return fmt.Errorf("select dependency outline: %w", err)
	}
	staged.OutlineBytes = selection.RenderedBytes
	staged.OutlineFiles = len(selection.Included)
	staged.OutlineOmittedFiles = len(selection.Omitted)
	meta["exported_symbols"] = countExported(res)
	meta["outline_bytes"] = staged.OutlineBytes
	meta["outline_files"] = staged.OutlineFiles
	meta["outline_omitted_files"] = staged.OutlineOmittedFiles
	if err := writeWorkspaceJSON(ws, "context.json", meta); err != nil {
		return err
	}
	if err := writeWorkspaceJSON(ws, outlineSelectionFile, selection); err != nil {
		return err
	}
	if err := writeWorkspaceFile(ws, "dep-outline.md", selection.contents); err != nil {
		return err
	}
	return nil
}

// checkoutVersion moves dir's working tree to the git tag matching version and
// returns a function that restores the previous ref, the matching tag, and
// whether checkout succeeded. Registries report versions with or without a
// leading v depending on ecosystem, and repositories tag with or without one
// independently of that, so both spellings are tried.
// The checkout is forced because StripDirectives from a prior run in the same
// --work directory can leave tracked deletions in the reused clone.
func checkoutVersion(ctx context.Context, dir, version, ecosystem string) (restore func(), tag string, matched bool) {
	noop := func() {}
	if version == "" {
		return noop, "", false
	}
	candidates, err := vers.TagCandidates(version, ecosystem)
	if err != nil {
		return noop, "", false
	}
	for _, tag := range candidates {
		restoreCheckout, err := clone.CheckoutTag(ctx, dir, tag)
		if err != nil {
			continue
		}
		return func() {
			_ = restoreCheckout(context.WithoutCancel(ctx))
		}, tag, true
	}
	return noop, "", false
}

// countExported returns the number of exported top-level declarations across
// all outlined files. Test files and files under obvious test directories are
// excluded so the count reflects the shipped surface.
func countExported(r *outline.Result) int {
	n := 0
	for _, f := range r.Files {
		if isTestPath(f.Path) {
			continue
		}
		for _, s := range f.Symbols {
			if s.Exported {
				n++
			}
		}
	}
	return n
}

func isTestPath(p string) bool {
	if strings.HasSuffix(p, "_test.go") {
		return true
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "test", "tests", "spec", "specs", "__tests__":
			return true
		}
	}
	return false
}

func lookupDependency(ctx context.Context, rc *registries.Client, d hyrum.Dep) (registryDependency, error) {
	if d.PURL == "" {
		return registryDependency{}, fmt.Errorf("no purl for %s", d.Name)
	}
	registry, name, _, err := registries.NewFromPURL(d.PURL, rc)
	if err != nil {
		return registryDependency{}, err
	}
	pkg, err := registry.FetchPackage(ctx, name)
	if err != nil {
		return registryDependency{}, err
	}
	info := registryDependency{Repository: pkg.Repository, Latest: pkg.LatestVersion}
	versions, err := registry.FetchVersions(ctx, name)
	if err != nil {
		info.BaselineError = fmt.Sprintf("fetch registry versions: %v", err)
		return info, nil
	}
	info.Baseline, err = resolveBaseline(d.Version, d.Ecosystem, versions)
	if err != nil {
		info.BaselineError = err.Error()
	}
	return info, nil
}

func resolveBaseline(constraint, ecosystem string, versions []registries.Version) (string, error) {
	if constraint == "" {
		return "", fmt.Errorf("manifest has no baseline constraint")
	}
	rangeValue, err := vers.ParseNative(constraint, ecosystem)
	if err != nil {
		return "", fmt.Errorf("parse baseline constraint %q: %w", constraint, err)
	}
	_, exact := rangeValue.ExactVersion()
	baseline := ""
	for _, version := range versions {
		if version.Number == "" {
			continue
		}
		if !exact && (version.Status == registries.StatusYanked || version.Status == registries.StatusRetracted) {
			continue
		}
		if !vers.ValidWithScheme(version.Number, ecosystem) || !rangeValue.Contains(version.Number) {
			continue
		}
		if baseline == "" || vers.CompareWithScheme(version.Number, baseline, ecosystem) < 0 {
			baseline = version.Number
		}
	}
	if baseline == "" {
		return "", fmt.Errorf("no usable registry release satisfies baseline constraint %q", constraint)
	}
	return baseline, nil
}

func selectDeps(t *hyrum.Target, names stringList) []hyrum.Dep {
	if len(names) == 0 {
		var out []hyrum.Dep
		for _, d := range t.Deps {
			if d.Direct && d.Scope != "development" && d.Scope != "dev" {
				out = append(out, d)
			}
		}
		return out
	}
	var out []hyrum.Dep
	for _, n := range names {
		if d, ok := findDep(t, n); ok {
			out = append(out, d)
		}
	}
	return out
}

// selectGenDeps applies dependency configuration to copies of the analyzed
// dependencies. Purl-specific values override name-specific values. Configured
// skips affect default generation only; explicit --dep selections always win.
func selectGenDeps(t *hyrum.Target, names stringList, overrides map[string]hyrumconfig.Dependency) []hyrum.Dep {
	selected := selectDeps(t, names)
	out := make([]hyrum.Dep, 0, len(selected))
	for _, dep := range selected {
		override := dependencyConfigFor(dep, overrides)
		if len(names) == 0 && override.Skip != nil && *override.Skip {
			continue
		}
		if override.Baseline != nil {
			dep.Version = *override.Baseline
		}
		out = append(out, dep)
	}
	return out
}

func dependencyConfigFor(dep hyrum.Dep, overrides map[string]hyrumconfig.Dependency) hyrumconfig.Dependency {
	override := overrides[dep.Name]
	byPURL, ok := overrides[dep.PURL]
	if !ok {
		return override
	}
	if byPURL.Baseline != nil {
		override.Baseline = byPURL.Baseline
	}
	if byPURL.Skip != nil {
		override.Skip = byPURL.Skip
	}
	if byPURL.Activations != nil {
		override.Activations = byPURL.Activations
	}
	return override
}

func writeJSONUnder(root, dir, name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = hyrum.WriteFilesUnder(root, dir, []hyrum.GeneratedFile{{Path: name, Content: string(b)}})
	return err
}
