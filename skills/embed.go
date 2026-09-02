// Package skills embeds the SKILL.md and schema.json bundles that the
// generation pipeline stages into a workspace before invoking a harness
// backend. Keeping them embedded means a built hyrum binary carries its own
// skill definitions and does not depend on a source checkout at run time.
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/alpha-omega-security/harness"
	"github.com/alpha-omega-security/hyrum/internal/safefs"
)

//go:embed */SKILL.md */schema.json
var FS embed.FS

// Stage copies skill name's files from the embedded bundle into the directory
// where backend h looks for them under workspace. It returns the destination
// directory.
func Stage(h harness.Harness, workspace, name string) (string, error) {
	dst := h.SkillDir(workspace, name)
	rel, err := filepath.Rel(workspace, dst)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("skill directory %q is outside workspace", dst)
	}
	root, err := safefs.Open(workspace)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll(rel, 0o755); err != nil {
		return "", err
	}
	entries, err := fs.ReadDir(FS, name)
	if err != nil {
		return "", fmt.Errorf("skill %q: %w", name, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := fs.ReadFile(FS, name+"/"+e.Name())
		if err != nil {
			return "", err
		}
		if err := root.WriteFile(filepath.Join(rel, e.Name()), b, 0o644); err != nil {
			return "", err
		}
	}
	return dst, nil
}
