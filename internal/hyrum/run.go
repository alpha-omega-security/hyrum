package hyrum

import (
	"context"
	"encoding/json"
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

// RunResult carries the skill output plus what the backend reported about the
// invocation.
type RunResult struct {
	Output    GenerateResult
	CostUSD   float64
	Turns     int
	SessionID string
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

	if err := harness.Run(ctx, h, job, wrapped); err != nil {
		return nil, err
	}

	outPath := filepath.Join(ws, outputFile)
	b, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w (skill did not write output)", outputFile, err)
	}
	if err := json.Unmarshal(b, &res.Output); err != nil {
		return nil, fmt.Errorf("parse %s: %w", outputFile, err)
	}
	return res, nil
}

// WriteFiles writes each generated file under root, refusing paths that escape
// it. It returns the list of written paths.
func WriteFiles(root string, files []GeneratedFile) ([]string, error) {
	var written []string
	for _, f := range files {
		clean := filepath.Clean(f.Path)
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return written, fmt.Errorf("refusing path %q", f.Path)
		}
		dst := filepath.Join(root, clean)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return written, err
		}
		if err := os.WriteFile(dst, []byte(f.Content), 0o644); err != nil {
			return written, err
		}
		written = append(written, dst)
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
