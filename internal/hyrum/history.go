package hyrum

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/git-pkgs/changelog"
	"github.com/git-pkgs/purl"
	"github.com/git-pkgs/vulns"
	"github.com/git-pkgs/vulns/osv"
)

// Commit is one git-log record from the target's history.
type Commit struct {
	SHA     string `json:"sha"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
	Body    string `json:"body,omitempty"`
	// Files is populated for the manifest-path scan so hyrum-history can see
	// which lockfile changed without opening the commit.
	Files []string `json:"files,omitempty"`
}

// HistoryIndex holds the result of one full-history scan of the target,
// partitioned so per-dependency git-log.txt files can be written without
// re-invoking git for each dependency.
type HistoryIndex struct {
	// byName maps a lowercase dependency name to the commits whose subject
	// or body mentions it.
	byName map[string][]Commit
	// manifest holds commits that touched a manifest or lockfile path,
	// regardless of message.
	manifest []Commit
}

// BuildHistoryIndex runs two streamed git-log passes over the target's full
// history and partitions the results by dependency name.
//
// The first pass streams `git log --all` with NUL/US separators and matches
// each commit's subject+body against every name in deps as a case-insensitive
// literal substring. The second pass streams `git log --all -- <manifest
// paths>` (paths taken from brief's package-manager detections) with
// --name-only so version-bump commits are captured even when the message
// does not name the dependency.
//
// Both passes propagate context cancellation. Zero matches for a name is a
// valid empty result.
func BuildHistoryIndex(ctx context.Context, t *Target, deps []Dep) (*HistoryIndex, error) {
	needles := make([]string, 0, len(deps))
	for _, d := range deps {
		needles = append(needles, strings.ToLower(d.Name))
	}
	idx := &HistoryIndex{byName: make(map[string][]Commit, len(needles))}

	err := streamLog(ctx, t.Path, nil, false, func(c Commit) {
		hay := strings.ToLower(c.Subject + "\n" + c.Body)
		for _, n := range needles {
			if strings.Contains(hay, n) {
				idx.byName[n] = append(idx.byName[n], c)
			}
		}
	})
	if err != nil {
		return nil, fmt.Errorf("git log (messages): %w", err)
	}

	paths := manifestPaths(t)
	if len(paths) > 0 {
		err = streamLog(ctx, t.Path, paths, true, func(c Commit) {
			idx.manifest = append(idx.manifest, c)
		})
		if err != nil {
			return nil, fmt.Errorf("git log (manifests): %w", err)
		}
	}
	return idx, nil
}

// For returns the commits relevant to dep: message matches merged with
// manifest-path commits whose patch mentions dep, deduplicated by SHA in
// git's original ordering (message matches first, then any manifest-only
// commits appended).
func (h *HistoryIndex) For(dep string) []Commit {
	name := strings.ToLower(dep)
	seen := map[string]bool{}
	out := make([]Commit, 0, len(h.byName[name]))
	for _, c := range h.byName[name] {
		if !seen[c.SHA] {
			seen[c.SHA] = true
			out = append(out, c)
		}
	}
	for _, c := range h.manifest {
		if seen[c.SHA] {
			continue
		}
		// A manifest commit is relevant when the diff mentions the dep name.
		// Checking here rather than during the scan keeps the scan O(commits)
		// instead of O(commits × deps); the manifest-touching set is small.
		if commitMentions(c, name) {
			seen[c.SHA] = true
			out = append(out, c)
		}
	}
	return out
}

func commitMentions(c Commit, name string) bool {
	// The manifest scan does not carry the patch body; the skill can
	// `git -C ./target show <sha>` for the ones it wants to inspect.
	// A commit is included here when its subject/body or its file list
	// mentions the name (e.g. a lockfile path like go.sum won't, but a
	// requirements-<name>.txt would).
	if strings.Contains(strings.ToLower(c.Subject+"\n"+c.Body), name) {
		return true
	}
	for _, f := range c.Files {
		if strings.Contains(strings.ToLower(f), name) {
			return true
		}
	}
	return false
}

// WriteGitLog writes the commits for dep from a pre-built index to path in
// the same `SHA date subject / body / ---` text format the hyrum-history
// skill reads.
func (h *HistoryIndex) WriteGitLog(dep, path string) error {
	var b strings.Builder
	for _, c := range h.For(dep) {
		fmt.Fprintf(&b, "%s %s %s\n", c.SHA, c.Date, c.Subject)
		if c.Body != "" {
			b.WriteString(c.Body)
			if !strings.HasSuffix(c.Body, "\n") {
				b.WriteByte('\n')
			}
		}
		if len(c.Files) > 0 {
			b.WriteString(strings.Join(c.Files, "\n"))
			b.WriteByte('\n')
		}
		b.WriteString("---\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// streamLog runs `git log --all` in repo and calls fn for each commit. paths
// restricts to commits touching those paths; nameOnly adds --name-only so
// Commit.Files is populated. The format uses NUL between records and US
// (0x1f) between fields so subjects and bodies containing --- or blank lines
// parse unambiguously.
func streamLog(ctx context.Context, repo string, paths []string, nameOnly bool, fn func(Commit)) error {
	// %x00 and %x1f in the format expand to NUL and US in git's output; the
	// argv string itself contains no control bytes.
	const rs, us = "\x00", "\x1f"
	args := []string{"-C", repo, "log", "--all", "--date=short",
		"--format=%H%x1f%ad%x1f%s%x1f%b%x00"}
	if nameOnly {
		args = append(args, "--name-only")
	}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	sc.Split(splitOn(rs[0]))
	for sc.Scan() {
		rec := sc.Text()
		// With --name-only, the file list follows the format string on
		// subsequent lines within the same NUL-terminated record.
		fields := strings.SplitN(rec, us, 4)
		if len(fields) < 4 {
			continue
		}
		body := fields[3]
		var files []string
		if nameOnly {
			// Body ends at the first \n\n; what follows is the file list.
			if i := strings.Index(body, "\n\n"); i >= 0 {
				for _, f := range strings.Split(strings.TrimSpace(body[i+2:]), "\n") {
					if f != "" {
						files = append(files, f)
					}
				}
				body = body[:i]
			} else if j := strings.LastIndexByte(body, '\n'); j >= 0 && body[:j] == "" {
				// Empty body: everything after the leading newline is files.
				for _, f := range strings.Split(strings.TrimSpace(body), "\n") {
					if f != "" {
						files = append(files, f)
					}
				}
				body = ""
			}
		}
		fn(Commit{
			SHA:     strings.TrimLeft(fields[0], "\n"),
			Date:    fields[1],
			Subject: fields[2],
			Body:    strings.TrimSpace(body),
			Files:   files,
		})
	}
	if err := sc.Err(); err != nil {
		_ = cmd.Wait()
		return err
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func splitOn(delim byte) bufio.SplitFunc {
	return func(data []byte, atEOF bool) (int, []byte, error) {
		if i := strings.IndexByte(string(data), delim); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	}
}

// manifestPaths returns the manifest and lockfile paths brief detected for
// the target's package managers, relative to the target root.
func manifestPaths(t *Target) []string {
	if t.Report == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, pm := range t.Report.PackageManagers {
		for _, f := range pm.ConfigFiles {
			if f != "" && !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
		if pm.Lockfile != "" && !seen[pm.Lockfile] {
			seen[pm.Lockfile] = true
			out = append(out, pm.Lockfile)
		}
	}
	return out
}

// GatherHistory writes the hyrum-history skill's input files into ws:
//
//	git-log.txt      target commits mentioning the dependency, from idx
//	changelog.json   parsed entries from the dep's changelog, when found
//	vulns.json       OSV advisories for the dep's purl
//
// idx is built once per gen invocation and shared across dependencies so
// git log runs twice total rather than once per dep. depDir is the dep's
// cloned source (for changelog discovery); pass "" to skip. Each output is
// best-effort: a missing changelog or an OSV error results in an absent
// file rather than a hard failure, because hyrum-history treats every input
// as optional.
func GatherHistory(ctx context.Context, idx *HistoryIndex, d Dep, depDir, ws string) error {
	if err := idx.WriteGitLog(d.Name, filepath.Join(ws, "git-log.txt")); err != nil {
		return err
	}
	if depDir != "" {
		writeChangelog(depDir, filepath.Join(ws, "changelog.json"))
	}
	writeVulns(ctx, d.PURL, filepath.Join(ws, "vulns.json"))
	return nil
}

type changelogEntry struct {
	Version string `json:"version"`
	Date    string `json:"date,omitempty"`
	Body    string `json:"body"`
}

func writeChangelog(depDir, out string) {
	p, err := changelog.FindAndParse(depDir)
	if err != nil || p == nil {
		return
	}
	var entries []changelogEntry
	for _, v := range p.Versions() {
		e, ok := p.Entry(v)
		if !ok {
			continue
		}
		date := ""
		if e.Date != nil {
			date = e.Date.Format("2006-01-02")
		}
		entries = append(entries, changelogEntry{Version: v, Date: date, Body: e.Content})
	}
	writeJSONFile(out, entries)
}

func writeVulns(ctx context.Context, purlStr, out string) {
	if purlStr == "" {
		return
	}
	p, err := purl.Parse(purlStr)
	if err != nil {
		return
	}
	src := osv.New()
	list, err := src.Query(ctx, p)
	if err != nil || len(list) == 0 {
		return
	}
	type v struct {
		ID       string   `json:"id"`
		Summary  string   `json:"summary"`
		Aliases  []string `json:"aliases,omitempty"`
		Severity string   `json:"severity,omitempty"`
		Fixed    string   `json:"fixed,omitempty"`
	}
	slim := make([]v, 0, len(list))
	for _, x := range list {
		slim = append(slim, v{
			ID:       x.ID,
			Summary:  x.Summary,
			Aliases:  x.Aliases,
			Severity: severityOf(x),
			Fixed:    x.FixedVersion(p.Type, p.Name),
		})
	}
	writeJSONFile(out, slim)
}

func severityOf(x vulns.Vulnerability) string {
	if s := x.SeverityLevel(); s != "" {
		return s
	}
	return ""
}

func writeJSONFile(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, b, 0o644)
}
