package hyrum

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/git-pkgs/changelog"
	"github.com/git-pkgs/purl"
	"github.com/git-pkgs/vulns"
	"github.com/git-pkgs/vulns/osv"
)

// GatherHistory writes the hyrum-history skill's input files into ws:
//
//	git-log.txt      target commits mentioning the dependency name
//	changelog.json   parsed entries from the dep's changelog, when found
//	vulns.json       OSV advisories for the dep's purl
//
// depDir is the dep's cloned source (for changelog discovery); pass "" to
// skip. Each output is best-effort: a missing changelog or an OSV error
// results in an absent file, not a hard failure, because hyrum-history
// treats every input as optional.
func GatherHistory(ctx context.Context, t *Target, d Dep, depDir, ws string) error {
	if err := writeGitLog(ctx, t.Path, d.Name, filepath.Join(ws, "git-log.txt")); err != nil {
		return err
	}
	if depDir != "" {
		writeChangelog(depDir, filepath.Join(ws, "changelog.json"))
	}
	writeVulns(ctx, d.PURL, filepath.Join(ws, "vulns.json"))
	return nil
}

func writeGitLog(ctx context.Context, repo, needle, out string) error {
	// --all so branches and tags are covered; %H/%s/%b so the skill can
	// `git -C ./target show <sha>` for any hit. --regexp-ignore-case because
	// package names are matched loosely in prose.
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "log", "--all",
		"--regexp-ignore-case", "--grep", needle,
		"--date=short", "--format=%H %ad %s%n%b%n---")
	b, err := cmd.Output()
	if err != nil {
		// A shallow clone or non-repo target still gets an empty file so the
		// skill sees "no hits" rather than "input missing".
		b = nil
	}
	return os.WriteFile(out, b, 0o644)
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
	// Trim to fields the skill cares about; full OSV records are large.
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
