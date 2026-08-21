// Package hyrum wires git-pkgs libraries into the Hyrum's-test generation
// pipeline. Nothing here is ecosystem-specific; per-ecosystem behaviour lives
// behind the usage.Indexer registry and the managers package.
package hyrum

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/git-pkgs/brief"
	"github.com/git-pkgs/brief/detect"
	"github.com/git-pkgs/brief/kb"
	"github.com/git-pkgs/manifests"
	"github.com/git-pkgs/purl"
)

// Target is a repository to generate Hyrum's tests for, plus everything
// discovered about it that later pipeline stages need.
type Target struct {
	Path   string
	Name   string
	Report *brief.Report
	Deps   []Dep
}

// Dep is one direct dependency of the target. PURL is the join key across
// registries, vulns, and the generated test layout. Ecosystem is derived from
// the PURL type so it always agrees with what registries/managers expect.
type Dep struct {
	Name      string
	Version   string
	PURL      string
	Ecosystem string
	Scope     string
	Direct    bool
}

// Analyze runs brief over path and returns a populated Target. brief already
// discovers manifests, parses them via git-pkgs/manifests, merges manifest and
// lockfile entries, and marks direct vs transitive; we take its list as-is and
// only add the ecosystem string parsed from each PURL.
func Analyze(path string) (*Target, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	kbase, err := kb.Load(brief.KnowledgeFS)
	if err != nil {
		return nil, fmt.Errorf("brief kb: %w", err)
	}
	rep, err := detect.New(kbase, abs).Run()
	if err != nil {
		return nil, fmt.Errorf("brief: %w", err)
	}

	deps := make([]Dep, 0, len(rep.Dependencies))
	for _, d := range rep.Dependencies {
		deps = append(deps, Dep{
			Name:      d.Name,
			Version:   d.Version,
			PURL:      d.PURL,
			Ecosystem: ecosystemOf(d.PURL),
			Scope:     d.Scope,
			Direct:    d.Direct,
		})
	}

	return &Target{Path: abs, Name: detectTargetName(abs, rep), Report: rep, Deps: deps}, nil
}

// detectTargetName returns one stable directory component for a target.
// Nested package targets include their Git-relative path in the identity.
func detectTargetName(targetPath string, report *brief.Report) string {
	repositoryRoot := findRepositoryRoot(targetPath)
	relativePath := repositoryRelativeTargetPath(repositoryRoot, targetPath)
	if name := manifestPackageName(targetPath); name != "" {
		if relativePath != "" {
			return normalizeTargetName(name) + "-" + targetNameHash(relativePath)
		}
		return normalizeTargetName(name)
	}

	name := reportRemoteName(report)
	if name == "" {
		if repositoryRoot != "" {
			name = filepath.Base(repositoryRoot)
		} else {
			name = filepath.Base(targetPath)
		}
	}
	if relativePath != "" {
		name += "/" + relativePath
	}
	return normalizeTargetName(name)
}

func repositoryRelativeTargetPath(repositoryRoot, targetPath string) string {
	if repositoryRoot == "" {
		return ""
	}
	relative, err := filepath.Rel(repositoryRoot, targetPath)
	if err != nil || relative == "." {
		return ""
	}
	return filepath.ToSlash(relative)
}

// manifestPackageName returns a package name only when the target has one
// unambiguous root manifest identity. Nested workspace members describe other
// packages and do not name the selected target directory.
func manifestPackageName(targetPath string) string {
	entries, err := os.ReadDir(targetPath)
	if err != nil {
		return ""
	}
	names := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		_, kind, ok := manifests.Identify(entry.Name())
		if !ok || kind != manifests.Manifest {
			continue
		}
		content, err := os.ReadFile(filepath.Join(targetPath, entry.Name()))
		if err != nil {
			continue
		}
		parsed, err := manifests.Parse(entry.Name(), content, manifests.Options{FSRoot: targetPath})
		if err != nil {
			continue
		}
		if name := strings.TrimSpace(parsed.Name); name != "" {
			names[name] = struct{}{}
		}
	}
	if len(names) != 1 {
		return ""
	}
	for name := range names {
		return name
	}
	return ""
}

func reportRemoteName(report *brief.Report) string {
	if report == nil || report.Git == nil {
		return ""
	}
	if remote := report.Git.Remotes["origin"]; remote != "" {
		return remoteBase(remote)
	}
	var remotes []string
	for _, remote := range report.Git.Remotes {
		if strings.HasPrefix(remote, "https://") {
			remotes = append(remotes, remote)
		}
	}
	sort.Strings(remotes)
	if len(remotes) == 0 {
		return ""
	}
	return remoteBase(remotes[0])
}

func remoteBase(remote string) string {
	base := filepath.Base(remote)
	return strings.TrimSuffix(base, ".git")
}

func findRepositoryRoot(targetPath string) string {
	current := filepath.Clean(targetPath)
	for {
		if _, err := os.Stat(filepath.Join(current, ".git")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

// normalizeTargetName converts package names and Git-relative fallback paths
// into one portable path component. A hash preserves identity when case or
// punctuation is changed during normalization.
func normalizeTargetName(value string) string {
	original := strings.TrimSpace(value)
	var normalized strings.Builder
	separator := false
	for _, char := range original {
		switch {
		case char >= 'A' && char <= 'Z':
			normalized.WriteRune(char + ('a' - 'A'))
			separator = false
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9', char == '-', char == '_', char == '.':
			normalized.WriteRune(char)
			separator = false
		default:
			if normalized.Len() > 0 && !separator {
				normalized.WriteByte('-')
				separator = true
			}
		}
	}
	name := strings.Trim(normalized.String(), "-._")
	if name == "" {
		name = "target"
	}
	if name == original {
		return name
	}
	return name + "-" + targetNameHash(original)
}

func targetNameHash(value string) string {
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
	return hash[:10]
}

func ecosystemOf(p string) string {
	u, err := purl.Parse(p)
	if err != nil {
		return ""
	}
	return u.Type
}

// Ecosystems returns the distinct ecosystems present in t.Deps, in first-seen
// order. Callers use this to pick usage indexers and package managers without
// assuming a single-language repo.
func (t *Target) Ecosystems() []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range t.Deps {
		if d.Ecosystem == "" || seen[d.Ecosystem] {
			continue
		}
		seen[d.Ecosystem] = true
		out = append(out, d.Ecosystem)
	}
	return out
}
