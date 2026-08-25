package hyrum

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/git-pkgs/changelog"
	"github.com/git-pkgs/purl"
	"github.com/git-pkgs/vers"
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
	// Changes contains manifest diff excerpts that matched the dependency.
	// Lockfile excerpts may include an unchanged package identity line.
	Changes []string `json:"changes,omitempty"`
	patch   string
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
// paths>` (paths taken from brief's package-manager detections) with patches
// so version-bump commits are captured even when the message does not name
// the dependency. Manifest matching considers changed lines and an immediately
// preceding package identity line when a lockfile separates name and version.
//
// Both passes propagate context cancellation. Zero matches for a name is a
// valid empty result.
func BuildHistoryIndex(ctx context.Context, t *Target, deps []Dep) (*HistoryIndex, error) {
	needles := make([]string, 0, len(deps))
	for _, d := range deps {
		needles = append(needles, strings.ToLower(d.Name))
	}
	idx := &HistoryIndex{byName: make(map[string][]Commit, len(needles))}

	err := streamLog(ctx, t.Path, nil, logMetadata, func(c Commit) {
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
		err = streamLog(ctx, t.Path, paths, logPatch, func(c Commit) {
			c.Files = touchedManifestPaths(c.patch, paths)
			c.Changes = changedPatchLines(c.patch)
			c.patch = ""
			idx.manifest = append(idx.manifest, c)
		})
		if err != nil {
			return nil, fmt.Errorf("git log (manifests): %w", err)
		}
	}
	return idx, nil
}

// For returns the commits relevant to dep: message matches merged with
// manifest-path commits whose diff excerpts mention dep, deduplicated by SHA
// in git's original ordering (message matches first, then any manifest-only
// commits appended).
func (h *HistoryIndex) For(dep string) []Commit {
	name := strings.ToLower(dep)
	manifestMatches := make(map[string]Commit)
	for _, c := range h.manifest {
		c.Changes = matchingChanges(c.Changes, name)
		if len(c.Changes) > 0 {
			manifestMatches[c.SHA] = c
		}
	}

	seen := map[string]bool{}
	out := make([]Commit, 0, len(h.byName[name]))
	for _, c := range h.byName[name] {
		if !seen[c.SHA] {
			if manifest, ok := manifestMatches[c.SHA]; ok {
				c.Files = manifest.Files
				c.Changes = manifest.Changes
			}
			seen[c.SHA] = true
			out = append(out, c)
		}
	}
	for _, c := range h.manifest {
		if seen[c.SHA] {
			continue
		}
		if match, ok := manifestMatches[c.SHA]; ok {
			seen[c.SHA] = true
			out = append(out, match)
		}
	}
	return out
}

func matchingChanges(changes []string, name string) []string {
	var matches []string
	for _, change := range changes {
		if strings.Contains(strings.ToLower(change), name) {
			matches = append(matches, change)
		}
	}
	return matches
}

// WriteGitLog writes the commits for dep from a pre-built index to path in
// the `SHA date subject / body / manifest paths / matching changes / ---`
// text format the hyrum-history skill reads.
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
		if len(c.Changes) > 0 {
			b.WriteString(strings.Join(c.Changes, "\n"))
			b.WriteByte('\n')
		}
		b.WriteString("---\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// maxLogRecord bounds a single git-log record (SHA + date + subject + body +
// optional detail). It exists so a repository with a multi-megabyte commit
// body or patch cannot exhaust memory via bufio.Scanner's growing buffer.
const maxLogRecord = 8 << 20

// logFields is the number of US-separated fields in the streamLog format
// string: SHA, date, subject, body.
const logFields = 4

type logDetail uint8

const (
	logMetadata logDetail = iota
	logNames
	logPatch
)

// parseLogRecord splits one NUL-delimited record from streamLog into a
// Commit. An RS byte separates the body from an optional name list or patch.
func parseLogRecord(rec, us, detailSep string, detail logDetail) (Commit, bool) {
	fields := strings.SplitN(rec, us, logFields)
	if len(fields) < logFields {
		return Commit{}, false
	}
	body := fields[3]
	var files []string
	var patch string
	if detail != logMetadata {
		body, patch = splitLogDetail(body, detailSep)
		if detail == logNames {
			files = nonEmptyLines(patch)
			patch = ""
		}
	}
	return Commit{
		SHA:     strings.TrimSpace(fields[0]),
		Date:    fields[1],
		Subject: fields[2],
		Body:    strings.TrimSpace(body),
		Files:   files,
		patch:   patch,
	}, true
}

// splitLogDetail separates the commit body from a trailing name list or patch
// using the explicit separator emitted by streamLog.
func splitLogDetail(body, detailSep string) (string, string) {
	before, after, ok := strings.Cut(body, detailSep)
	if !ok {
		return body, ""
	}
	return before, strings.TrimSpace(after)
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// streamLog runs `git log --all` in repo and calls fn for each commit. paths
// restricts the history. detail optionally includes a name list or patch. The
// format uses NUL between records, US (0x1f) between fields, and RS (0x1e)
// before detail so subjects and bodies containing --- or blank lines parse
// unambiguously.
func streamLog(ctx context.Context, repo string, paths []string, detail logDetail, fn func(Commit)) error {
	// The %xNN sequences expand to control bytes in git's output; the argv
	// string itself contains no control bytes.
	const rs, us, detailSep = "\x00", "\x1f", "\x1e"
	args := []string{"-C", repo, "log", "--all", "--date=short",
		"--format=%x00%H%x1f%ad%x1f%s%x1f%b"}
	if detail != logMetadata {
		args[len(args)-1] += "%x1e"
	}
	switch detail {
	case logNames:
		args = append(args, "--name-only")
	case logPatch:
		args = append(args, "--patch", "--unified=1")
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
	sc.Buffer(nil, maxLogRecord)
	sc.Split(splitOn(rs[0]))
	for sc.Scan() {
		if c, ok := parseLogRecord(sc.Text(), us, detailSep, detail); ok {
			fn(c)
		}
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

func touchedManifestPaths(patch string, paths []string) []string {
	var touched []string
	for _, path := range paths {
		plain := "diff --git a/" + path + " b/" + path
		quoted := "diff --git " + strconv.Quote("a/"+path) + " " + strconv.Quote("b/"+path)
		for _, line := range strings.Split(patch, "\n") {
			if line == plain || line == quoted {
				touched = append(touched, path)
				break
			}
		}
	}
	return touched
}

func changedPatchLines(patch string) []string {
	lines := strings.Split(patch, "\n")
	var changes []string
	for i, line := range lines {
		if isPatchChangeLine(line) {
			changes = append(changes, line)
		}
		if !isManifestIdentityContext(line) {
			continue
		}
		excerpt := []string{line}
		for j := i + 1; j < len(lines) && isPatchChangeLine(lines[j]); j++ {
			excerpt = append(excerpt, lines[j])
		}
		if len(excerpt) > 1 {
			changes = append(changes, strings.Join(excerpt, "\n"))
		}
	}
	return changes
}

func isPatchChangeLine(line string) bool {
	if strings.HasPrefix(line, "+++ ") || strings.HasPrefix(line, "--- ") {
		return false
	}
	return strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")
}

func isManifestIdentityContext(line string) bool {
	if !strings.HasPrefix(line, " ") {
		return false
	}
	text := strings.TrimSpace(line[1:])
	if strings.HasPrefix(text, "name = ") || strings.HasPrefix(text, `"name":`) {
		return true
	}
	return strings.HasSuffix(text, ":") ||
		(strings.HasSuffix(text, "{") && strings.Contains(text, ":"))
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
func GatherHistory(ctx context.Context, idx *HistoryIndex, d Dep, depDir, latest, ws string) error {
	if err := idx.WriteGitLog(d.Name, filepath.Join(ws, "git-log.txt")); err != nil {
		return err
	}
	if depDir != "" {
		writeChangelog(depDir, filepath.Join(ws, "changelog.json"), d.Version, latest)
	}
	writeVulns(ctx, d.PURL, filepath.Join(ws, "vulns.json"))
	return nil
}

type changelogEntry struct {
	Version string `json:"version"`
	Date    string `json:"date,omitempty"`
	Body    string `json:"body"`
}

func writeChangelog(depDir, out, from, to string) {
	p, err := changelog.FindAndParse(depDir)
	if err != nil || p == nil {
		return
	}
	// When both endpoints look like exact versions that could appear as
	// changelog headers, slice to the range so hyrum-history reads only
	// what changed between the target's baseline and latest instead of
	// every entry the file has.
	if isExactVersion(from) && isExactVersion(to) {
		if body, ok := p.Between(from, to); ok && body != "" {
			writeJSONFile(out, []changelogEntry{{Version: from + ".." + to, Body: body}})
			return
		}
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

// isExactVersion reports whether v is a single resolved version rather than a
// range or wildcard, so it can be matched against a changelog header.
func isExactVersion(v string) bool {
	c, err := vers.ParseConstraint(v)
	return err == nil && (c.Operator == "" || c.Operator == "=") && c.Version != "*"
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
