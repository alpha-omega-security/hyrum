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

// Decode unmarshals the skill's output file into v.
func (r *RunResult) Decode(v any) error {
	return json.Unmarshal(r.Output, v)
}

// RunSkill stages the named skill into ws, invokes backend h with harness.Run,
// and reads back the JSON output file. Events are printed to stderr as a
// simple progress log; a caller wanting structured events can pass its own
// emit via RunSkillWithEmit.
func RunSkill(ctx context.Context, h harness.Harness, ws, name, outputFile string) (*RunResult, error) {
	return RunSkillWithEmit(ctx, h, ws, name, outputFile, defaultEmit)
}

// RunSkillWithEmit is RunSkill with a caller-provided event sink.
func RunSkillWithEmit(ctx context.Context, h harness.Harness, ws, name, outputFile string, emit func(harness.Event)) (*RunResult, error) {
	skillDir, err := skills.Stage(h, ws, name)
	if err != nil {
		return nil, fmt.Errorf("stage skill: %w", err)
	}
	// SKILL.md files reference ./schema.json relative to the workspace root;
	// the backend discovers the skill under SkillDir. Mirror the schema so
	// the path in the instructions is correct regardless of backend layout.
	if b, err := os.ReadFile(filepath.Join(skillDir, "schema.json")); err == nil {
		_ = os.WriteFile(filepath.Join(ws, "schema.json"), b, 0o644)
	}

	job := harness.Job{
		Workspace:    ws,
		SrcDir:       "target",
		SkillName:    name,
		OutputFile:   outputFile,
		SystemPrompt: headlessSystemPrompt,
	}
	outputPath, err := prepareOutput(ws, outputFile)
	if err != nil {
		return nil, err
	}

	// Backends that do not report a price (codex) still report token usage;
	// fall back to the list-price estimate for the backend's default model so
	// meta.json carries something better than zero.
	model := ""
	if defs := h.DefaultModels(); len(defs) > 0 {
		model = defs[0].ID
	}

	res := &RunResult{}
	wrapped := func(e harness.Event) {
		switch e.Kind {
		case harness.KindResult:
			cost := e.CostUSD
			if cost == 0 && model != "" {
				cost = harness.CostFromUsage(model, e.Usage)
			}
			res.CostUSD = cost
			res.Turns = e.Turns
		case harness.KindSession:
			res.SessionID = e.SessionID
		}
		if emit != nil {
			emit(e)
		}
	}

	runErr := harness.Run(ctx, h, job, wrapped)
	return finishRun(ctx, res, outputPath, name, outputFile, runErr)
}

// prepareOutput removes the expected artifact before invoking a backend. Skill
// workspaces are intentionally reusable, so this prevents a failed invocation
// from appearing successful by leaving an older output file in place.
func prepareOutput(ws, outputFile string) (string, error) {
	clean := filepath.Clean(outputFile)
	if outputFile != clean || clean == "." || clean == ".." || clean != filepath.Base(clean) || filepath.IsAbs(clean) {
		return "", fmt.Errorf("refusing output path %q", outputFile)
	}
	path := filepath.Join(ws, clean)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("remove stale %s: %w", outputFile, err)
	}
	return path, nil
}

// finishRun reads the artifact written by this invocation. A non-zero backend
// exit is recoverable only when a fresh, usable JSON artifact exists; otherwise
// the backend error remains fatal. The backend failure is retained on a
// successful result so callers do not silently treat a partial run as clean.
func finishRun(ctx context.Context, res *RunResult, outputPath, skillName, outputFile string, runErr error) (*RunResult, error) {
	if runErr != nil {
		var accountErr *harness.AccountError
		if ctx.Err() != nil || errors.As(runErr, &accountErr) {
			return nil, runErr
		}
	}

	b, err := os.ReadFile(outputPath)
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
	cleanDir := filepath.Clean(dir)
	if !filepath.IsLocal(cleanDir) {
		return nil, fmt.Errorf("refusing output directory %q", dir)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	outputRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = outputRoot.Close() }()
	if err := outputRoot.MkdirAll(cleanDir, 0o755); err != nil {
		return nil, err
	}

	var written []string
	for _, f := range files {
		clean := filepath.Clean(f.Path)
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return written, fmt.Errorf("refusing path %q", f.Path)
		}
		rel := filepath.Join(cleanDir, clean)
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
		fmt.Fprintf(os.Stderr, "  · %s %s\n", e.Tool, firstLine(e.Text))
	case harness.KindText:
		fmt.Fprintf(os.Stderr, "  %s\n", firstLine(e.Text))
	case harness.KindError:
		fmt.Fprintf(os.Stderr, "  ! %s\n", e.Text)
	case harness.KindResult:
		fmt.Fprintf(os.Stderr, "  = %d turns, $%.4f\n", e.Turns, e.CostUSD)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
