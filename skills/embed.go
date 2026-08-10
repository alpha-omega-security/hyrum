// Package skills embeds the SKILL.md and schema.json bundles that the
// generation pipeline stages into a workspace before invoking a harness
// backend. Keeping them embedded means a built hyrum binary carries its own
// skill definitions and does not depend on a source checkout at run time.
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/alpha-omega-security/harness"
)

//go:embed */SKILL.md */schema.json
var FS embed.FS

// Stage copies skill name's files from the embedded bundle into the directory
// where backend h looks for them under workspace. It returns the destination
// directory.
func Stage(h harness.Harness, workspace, name string) (string, error) {
	dst := h.SkillDir(workspace, name)
	if err := os.MkdirAll(dst, 0o755); err != nil {
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
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o644); err != nil {
			return "", err
		}
	}
	return dst, nil
}
