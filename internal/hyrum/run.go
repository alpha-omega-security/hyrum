package hyrum

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alpha-omega-security/harness"
	"github.com/alpha-omega-security/hyrum/internal/safefs"
	"github.com/alpha-omega-security/hyrum/internal/terminal"
	"github.com/alpha-omega-security/hyrum/skills"
)

// GeneratedFile is one entry in a skill's tests.json output.
type GeneratedFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Source  string `json:"source,omitempty"`
}

// GenerateResult is the parsed tests.json from the hyrum-generate skill.
type GenerateResult struct {
	Files []GeneratedFile `json:"files"`
	Notes string          `json:"notes,omitempty"`
}

// Verdict is one entry in verdict.json from the hyrum-validate skill.
type Verdict struct {
	Test       string `json:"test"`
	File       string `json:"file,omitempty"`
	Status     string `json:"status"`
	Action     string `json:"action"`
	Reasoning  string `json:"reasoning"`
	Suggestion string `json:"suggestion,omitempty"`
}

// ValidateResult is the parsed verdict.json from the hyrum-validate skill.
type ValidateResult struct {
	Verdicts []Verdict `json:"verdicts"`
	Notes    string    `json:"notes,omitempty"`
}

// RunResult carries the skill output plus what the backend reported about the
// invocation. Output is the raw JSON body of the skill's output file; use
// Decode to unmarshal it into the shape the invoked skill produces.
type RunResult struct {
	Output    json.RawMessage
	CostUSD   float64
	Turns     int
	SessionID string
	// BackendError records a non-zero backend exit when the same invocation
	// still produced a fresh, usable output artifact. It is a categorical,
	// safe-to-display warning; raw provider output is never stored here.
	BackendError string
}

const recoveredBackendError = "backend exited non-zero after writing fresh output"

// TargetSubdir is the workspace subdirectory the target repository is
// symlinked (host) or bind-mounted (container) into. SKILL.md files refer to
// it by this name.
const TargetSubdir = "target"

// RunOptions contains backend-neutral per-invocation settings.
type RunOptions struct {
	Model    string
	MaxTurns int
}

// Decode unmarshals the skill's output file into v.
func (r *RunResult) Decode(v any) error {
	return json.Unmarshal(r.Output, v)
}

// RunSkill stages the named skill into ws, invokes backend h with harness.Run,
// and reads back the JSON output file. Events are printed to stderr as a
// simple progress log; a caller wanting structured events can pass its own
// emit via RunSkillWithEmit.
func RunSkill(ctx context.Context, h harness.Harness, ws, name, outputFile string) (*RunResult, error) {
	return RunSkillWithOptions(ctx, h, ws, name, outputFile, RunOptions{})
}

// RunSkillWithOptions is RunSkill with backend-neutral invocation settings.
func RunSkillWithOptions(ctx context.Context, h harness.Harness, ws, name, outputFile string, opts RunOptions) (*RunResult, error) {
	return runSkill(ctx, h, ws, name, outputFile, opts, defaultEmit)
}

// RunSkillWithEmit is RunSkill with a caller-provided event sink.
func RunSkillWithEmit(ctx context.Context, h harness.Harness, ws, name, outputFile string, emit func(harness.Event)) (*RunResult, error) {
	return runSkill(ctx, h, ws, name, outputFile, RunOptions{}, emit)
}

func runSkill(ctx context.Context, h harness.Harness, ws, name, outputFile string, opts RunOptions, emit func(harness.Event)) (*RunResult, error) {
	workspace, err := safefs.Open(ws)
	if err != nil {
		return nil, fmt.Errorf("open workspace: %w", err)
	}
	defer func() { _ = workspace.Close() }()
	if err := stageSkillFiles(workspace, h, ws, name); err != nil {
		return nil, err
	}

	job := harness.Job{
		Workspace:    ws,
		SrcDir:       TargetSubdir,
		SkillName:    name,
		OutputFile:   outputFile,
		SystemPrompt: headlessSystemPrompt,
		Model:        opts.Model,
		MaxTurns:     opts.MaxTurns,
	}
	if err := stageSystemPrompt(workspace, h, &job); err != nil {
		return nil, err
	}
	outputName, err := prepareOutput(workspace, outputFile)
	if err != nil {
		return nil, err
	}

	// Backends that do not report a price (codex) still report token usage;
	// fall back to the list-price estimate for the backend's default model so
	// meta.json carries something better than zero.
	model := opts.Model
	if model == "" {
		if defs := h.DefaultModels(); len(defs) > 0 {
			model = defs[0].ID
		}
	}

	res := &RunResult{}
	wrapped := func(e harness.Event) {
		recordRunEvent(res, model, e)
		if emit != nil {
			emit(e)
		}
	}

	runErr := harness.Run(ctx, h, job, wrapped)
	return finishRun(ctx, res, workspace, outputName, name, outputFile, runErr)
}

func stageSkillFiles(workspace *safefs.Root, h harness.Harness, ws, name string) error {
	skillDir, err := skills.Stage(h, ws, name)
	if err != nil {
		return fmt.Errorf("stage skill: %w", err)
	}
	// SKILL.md files reference schema.json at the workspace root while the
	// backend discovers the skill under SkillDir. Mirror the schema so the
	// path in the instructions is correct regardless of backend layout.
	workspaceAbs, workspaceErr := filepath.Abs(workspace.Path())
	skillDirAbs, skillErr := filepath.Abs(skillDir)
	if workspaceErr != nil || skillErr != nil {
		return nil
	}
	schemaRel, err := filepath.Rel(workspaceAbs, filepath.Join(skillDirAbs, "schema.json"))
	if err != nil || !filepath.IsLocal(schemaRel) {
		return nil
	}
	b, err := workspace.ReadRegular(schemaRel)
	if err != nil {
		return nil
	}
	if err := workspace.WriteFile("schema.json", b, 0o644); err != nil {
		return fmt.Errorf("stage schema: %w", err)
	}
	return nil
}

func recordRunEvent(result *RunResult, model string, event harness.Event) {
	switch event.Kind {
	case harness.KindResult:
		cost := event.CostUSD
		if cost == 0 && model != "" {
			cost = harness.CostFromUsage(model, event.Usage)
		}
		result.CostUSD = cost
		result.Turns = event.Turns
	case harness.KindSession:
		result.SessionID = event.SessionID
	}
}

func stageSystemPrompt(workspace *safefs.Root, h harness.Harness, job *harness.Job) error {
	if strings.TrimSpace(job.SystemPrompt) == "" || h.SystemPromptViaArgs() {
		return nil
	}
	guide := h.GuideFilename()
	if guide == "" || !filepath.IsLocal(guide) {
		return fmt.Errorf("harness: invalid guide filename %q", guide)
	}
	content := job.SystemPrompt
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := workspace.WriteFile(guide, []byte(content), 0o644); err != nil {
		return fmt.Errorf("harness: write %s: %w", guide, err)
	}
	// harness.Run calls WriteSystemPrompt itself. Clearing this field after
	// safely staging the guide prevents that call from reopening a planted
	// symlink with os.WriteFile. File-based backends read the staged guide.
	job.SystemPrompt = ""
	return nil
}

// prepareOutput removes the expected artifact before invoking a backend. Skill
// workspaces are intentionally reusable, so this prevents a failed invocation
// from appearing successful by leaving an older output file in place.
func prepareOutput(workspace *safefs.Root, outputFile string) (string, error) {
	clean := filepath.Clean(outputFile)
	if outputFile != clean || clean == "." || clean == ".." || clean != filepath.Base(clean) || filepath.IsAbs(clean) {
		return "", fmt.Errorf("refusing output path %q", outputFile)
	}
	if err := workspace.Remove(clean); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("remove stale %s: %w", outputFile, err)
	}
	return clean, nil
}

// finishRun reads the artifact written by this invocation. A non-zero backend
// exit is recoverable only when a fresh, usable JSON artifact exists; otherwise
// the backend error remains fatal. The backend failure is retained on a
// successful result so callers do not silently treat a partial run as clean.
func finishRun(ctx context.Context, res *RunResult, workspace *safefs.Root, outputName, skillName, outputFile string, runErr error) (*RunResult, error) {
	if runErr != nil {
		var accountErr *harness.AccountError
		if ctx.Err() != nil || errors.As(runErr, &accountErr) {
			return nil, runErr
		}
	}

	b, err := workspace.ReadRegular(outputName)
	if err != nil {
		outputErr := fmt.Errorf("read %s: %w (skill did not write output)", outputFile, err)
		if runErr != nil {
			return nil, fmt.Errorf("%w; %v", runErr, outputErr)
		}
		return nil, outputErr
	}
	if !json.Valid(b) {
		outputErr := fmt.Errorf("%s is not valid JSON", outputFile)
		if runErr != nil {
			return nil, fmt.Errorf("%w; %v", runErr, outputErr)
		}
		return nil, outputErr
	}
	if runErr != nil {
		if err := validateRecoveredOutput(skillName, b); err != nil {
			return nil, fmt.Errorf("%w; %v", runErr, err)
		}
	}

	res.Output = b
	if runErr != nil {
		res.BackendError = recoveredBackendError
	}
	return res, nil
}

func validateRecoveredOutput(skillName string, b []byte) error {
	switch skillName {
	case "hyrum-generate":
		var gen GenerateResult
		if err := json.Unmarshal(b, &gen); err != nil {
			return fmt.Errorf("decode recovered generate output: %w", err)
		}
		if len(gen.Files) == 0 {
			return fmt.Errorf("recovered generate output has no files")
		}
	case "hyrum-validate":
		var validate ValidateResult
		if err := json.Unmarshal(b, &validate); err != nil {
			return fmt.Errorf("decode recovered validate output: %w", err)
		}
		if len(validate.Verdicts) == 0 {
			return fmt.Errorf("recovered validate output has no verdicts")
		}
	}
	return nil
}

// WriteFiles writes each generated file directly under root.
func WriteFiles(root string, files []GeneratedFile) ([]string, error) {
	return WriteFilesUnder(root, ".", files)
}

// WriteFilesUnder writes each generated file under dir within root, refusing
// paths and symlinks that escape root. It returns the list of written paths.
func WriteFilesUnder(root, dir string, files []GeneratedFile) ([]string, error) {
	return writeFilesUnder(root, dir, files, false)
}

// ReplaceFilesUnder removes dir within root after validating all generated
// paths, then writes the current files. This prevents files omitted by a later
// generation from remaining in the suite.
func ReplaceFilesUnder(root, dir string, files []GeneratedFile) ([]string, error) {
	return writeFilesUnder(root, dir, files, true)
}

func writeFilesUnder(root, dir string, files []GeneratedFile, replace bool) ([]string, error) {
	cleanDir := filepath.Clean(dir)
	if !filepath.IsLocal(cleanDir) {
		return nil, fmt.Errorf("refusing output directory %q", dir)
	}
	if replace && cleanDir == "." {
		return nil, fmt.Errorf("refusing to replace output root")
	}
	rels := make([]string, len(files))
	for i, f := range files {
		clean := filepath.Clean(f.Path)
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return nil, fmt.Errorf("refusing path %q", f.Path)
		}
		rels[i] = filepath.Join(cleanDir, clean)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	outputRoot, err := safefs.Open(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = outputRoot.Close() }()
	if replace {
		if err := outputRoot.RemoveAll(cleanDir); err != nil {
			return nil, err
		}
	}
	if err := outputRoot.MkdirAll(cleanDir, 0o755); err != nil {
		return nil, err
	}

	var written []string
	for i, f := range files {
		rel := rels[i]
		if err := outputRoot.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
			return written, err
		}
		if err := outputRoot.WriteFile(rel, []byte(f.Content), 0o644); err != nil {
			return written, err
		}
		written = append(written, filepath.Join(root, rel))
	}
	return written, nil
}

// headlessSystemPrompt is written to the backend's project-instructions file
// (AGENTS.md, CLAUDE.md, etc.) in the workspace. Backends read the user's
// global agent config from HOME; a rule written for interactive sessions
// ("stop and ask on error", "confirm before X") would otherwise leave a
// headless run waiting on input that never arrives.
const headlessSystemPrompt = `This is a non-interactive batch run with no human available.

Questions, confirmation prompts, and permission requests cannot be answered. If a command fails, correct it and retry. Complete the named skill and write its output file; exiting without that file is treated as a failure.
`

func defaultEmit(e harness.Event) {
	switch e.Kind {
	case harness.KindThinking:
		// suppressed
	case harness.KindTool:
		fmt.Fprintf(os.Stderr, "  · %s %s\n", terminal.SingleLine(e.Tool), terminal.SingleLine(e.Text))
	case harness.KindText:
		fmt.Fprintf(os.Stderr, "  %s\n", terminal.SingleLine(e.Text))
	case harness.KindError:
		fmt.Fprintf(os.Stderr, "  ! %s\n", terminal.SingleLine(e.Text))
	case harness.KindResult:
		fmt.Fprintf(os.Stderr, "  = %d turns, $%.4f\n", e.Turns, e.CostUSD)
	}
}
