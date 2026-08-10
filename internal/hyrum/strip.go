package hyrum

// Lifted from alpha-omega-security/scrutineer internal/worker/strip.go.
// Candidate for extraction into harness, which already owns per-backend
// GuideFilename/SkillDir and is the natural home for "files agent CLIs
// auto-load as instructions".

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var agentDirectiveDirs = []string{
	".claude", ".anthropic", ".cursor", ".windsurf", ".continue",
	".cline", ".roo", ".goose", ".aider", ".aider.*", ".gemini",
	".codex", ".copilot", ".devin",
}

var agentDirectiveFiles = []string{
	"claude.md", "claude.*.md", "agents.md", "agent.md", "gemini.md",
	"codex.md", ".cursorrules", ".cursorignore", ".windsurfrules",
	".clinerules", ".roorules", ".rooignore", ".aider.conf.yml",
	".aider.conf.yaml", ".aiderrules", "copilot-instructions.md",
	"*.instructions.md", "*.prompt.md", ".rules", "llms.txt", "llms-full.txt",
}

// StripAgentDirectives removes every file or directory under root whose
// basename matches a known agent-CLI auto-load path, so a cloned repository
// cannot inject standing instructions into the skill that reads it. .git is
// skipped. Returns the number of items removed.
func StripAgentDirectives(root string) (int, error) {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		base := strings.ToLower(d.Name())
		if d.IsDir() && base == ".git" {
			return filepath.SkipDir
		}
		if matchAnyBasename(agentDirectiveDirs, base) {
			if rmErr := os.RemoveAll(p); rmErr != nil {
				return rmErr
			}
			n++
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if matchAnyBasename(agentDirectiveFiles, base) {
			if rmErr := os.Remove(p); rmErr != nil {
				return rmErr
			}
			n++
		}
		return nil
	})
	return n, err
}

func matchAnyBasename(patterns []string, base string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, base); ok {
			return true
		}
	}
	return false
}
