package main

import (
	"bytes"
	"fmt"
	pathpkg "path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alpha-omega-security/hyrum/internal/hyrum/usage"
	"github.com/git-pkgs/outline"
)

const (
	defaultOutlineBytes  = 256 << 10
	outlineSelectionFile = "outline-selection.json"
)

var outlineEntryNames = map[string]bool{
	"__init__.py":  true,
	"__init__.pyi": true,
	"index.js":     true,
	"index.jsx":    true,
	"index.mjs":    true,
	"index.cjs":    true,
	"index.ts":     true,
	"index.tsx":    true,
	"index.mts":    true,
	"index.cts":    true,
	"lib.rs":       true,
	"mod.rs":       true,
}

var outlineContextNames = map[string]bool{
	"readme":         true,
	"package.json":   true,
	"pyproject.toml": true,
	"setup.py":       true,
	"setup.cfg":      true,
	"go.mod":         true,
	"cargo.toml":     true,
	"mix.exs":        true,
	"composer.json":  true,
}

type outlineFileDecision struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type outlineSelection struct {
	BudgetBytes   int                   `json:"budget_bytes"`
	RenderedBytes int                   `json:"rendered_bytes"`
	Included      []outlineFileDecision `json:"included_files"`
	Omitted       []outlineFileDecision `json:"omitted_files"`
	contents      []byte
}

type outlineCandidate struct {
	path     string
	reason   string
	required bool
}

type dependencyOutlineIndex struct {
	files        []outline.File
	byPath       map[string]outline.File
	entryByDir   map[string][]string
	declarations map[string][]string
}

type outlineBudgetSelection struct {
	index            *dependencyOutlineIndex
	maxBytes         int
	selected         map[string]string
	rejectedByBudget map[string]bool
	entryBytes       map[string][]byte
	entrySize        int
}

// selectDependencyOutline keeps complete outlined files related to the
// observed usage surface and returns a byte-bounded Markdown document. Files
// that directly declare used symbols and their package entry points must fit;
// referenced declarations and package context use the remaining budget.
func selectDependencyOutline(
	packed *outline.Result,
	surface *usage.Surface,
	maxBytes int,
) (*outlineSelection, error) {
	if err := validateOutlineSelectionInput(packed, maxBytes); err != nil {
		return nil, err
	}
	index := indexDependencyOutline(packed.Files)
	candidates, directPaths := requiredOutlineCandidates(index, surface)
	budget := newOutlineBudgetSelection(index, maxBytes)
	if err := budget.addCandidates(sortedOutlineCandidates(candidates, true)); err != nil {
		return nil, err
	}
	if err := addReferencedOutlineCandidates(budget, directPaths); err != nil {
		return nil, err
	}
	if err := budget.addCandidates(outlineContextCandidates(index.files, len(budget.selected) == 0)); err != nil {
		return nil, err
	}
	if len(budget.selected) == 0 && hasUsableOutlineFile(index.files) {
		return nil, fmt.Errorf("outline byte budget %d cannot fit any selected dependency source file", maxBytes)
	}
	return budget.result()
}

func validateOutlineSelectionInput(packed *outline.Result, maxBytes int) error {
	if maxBytes <= 0 {
		return fmt.Errorf("outline byte budget must be greater than zero")
	}
	if packed == nil {
		return fmt.Errorf("outline result is nil")
	}
	return nil
}

func indexDependencyOutline(packedFiles []outline.File) *dependencyOutlineIndex {
	index := &dependencyOutlineIndex{
		files:        append([]outline.File(nil), packedFiles...),
		byPath:       make(map[string]outline.File, len(packedFiles)),
		entryByDir:   map[string][]string{},
		declarations: map[string][]string{},
	}
	sort.Slice(index.files, func(i, j int) bool { return index.files[i].Path < index.files[j].Path })
	for _, file := range index.files {
		index.add(file)
	}
	for dir := range index.entryByDir {
		sort.Strings(index.entryByDir[dir])
	}
	for name := range index.declarations {
		sort.Strings(index.declarations[name])
	}
	return index
}

func (index *dependencyOutlineIndex) add(file outline.File) {
	index.byPath[file.Path] = file
	if !outlineFileUsable(file) {
		return
	}
	if outlineEntryNames[strings.ToLower(pathpkg.Base(file.Path))] {
		dir := pathpkg.Dir(file.Path)
		index.entryByDir[dir] = append(index.entryByDir[dir], file.Path)
	}
	for _, symbol := range file.Symbols {
		if symbol.Exported && symbol.Name != "" {
			index.declarations[symbol.Name] = append(index.declarations[symbol.Name], file.Path)
		}
	}
}

func requiredOutlineCandidates(
	index *dependencyOutlineIndex,
	surface *usage.Surface,
) (map[string]outlineCandidate, map[string]string) {
	candidates := map[string]outlineCandidate{}
	referencePaths := addDirectOutlineCandidates(index.files, outlineUsageIdentifiers(surface), candidates)
	addAncestorOutlineEntries(index.entryByDir, candidates)
	addObservedModuleCandidates(index.files, outlineModuleNames(surface), candidates, referencePaths)
	return candidates, referencePaths
}

func addDirectOutlineCandidates(
	files []outline.File,
	used map[string]bool,
	candidates map[string]outlineCandidate,
) map[string]string {
	directPaths := map[string]string{}
	for _, file := range files {
		if !outlineFileUsable(file) {
			continue
		}
		for _, symbol := range file.Symbols {
			if symbol.Exported && used[symbol.Name] {
				reason := "declares used symbol " + symbol.Name
				addRequiredOutlineCandidate(candidates, file.Path, reason)
				directPaths[file.Path] = reason
				break
			}
		}
	}
	return directPaths
}

func addAncestorOutlineEntries(entryByDir map[string][]string, candidates map[string]outlineCandidate) {
	for _, candidate := range sortedOutlineCandidates(candidates, true) {
		for dir := pathpkg.Dir(candidate.path); ; dir = pathpkg.Dir(dir) {
			for _, entry := range entryByDir[dir] {
				addRequiredOutlineCandidate(candidates, entry, "package entry point for "+candidate.path)
			}
			if dir == "." || dir == "/" {
				break
			}
		}
	}
}

func addObservedModuleCandidates(
	files []outline.File,
	modules map[string]bool,
	candidates map[string]outlineCandidate,
	referencePaths map[string]string,
) {
	seedReferences := len(referencePaths) == 0
	for _, file := range files {
		if !outlineFileUsable(file) {
			continue
		}
		base := strings.ToLower(pathpkg.Base(file.Path))
		if outlineEntryNames[base] && outlinePathMatchesNames(file.Path, modules) {
			reason := "package entry point for observed module"
			addRequiredOutlineCandidate(candidates, file.Path, reason)
			if seedReferences {
				referencePaths[file.Path] = reason
			}
		}
		if file.Outlined && outlineStemMatchesNames(file.Path, modules) {
			reason := "source entry point for observed module"
			addRequiredOutlineCandidate(candidates, file.Path, reason)
			if seedReferences {
				referencePaths[file.Path] = reason
			}
		}
	}
}

func newOutlineBudgetSelection(index *dependencyOutlineIndex, maxBytes int) *outlineBudgetSelection {
	return &outlineBudgetSelection{
		index:            index,
		maxBytes:         maxBytes,
		selected:         map[string]string{},
		rejectedByBudget: map[string]bool{},
		entryBytes:       map[string][]byte{},
	}
}

func (budget *outlineBudgetSelection) addCandidates(candidates []outlineCandidate) error {
	for _, candidate := range candidates {
		if err := budget.add(candidate); err != nil {
			return err
		}
	}
	return nil
}

func (budget *outlineBudgetSelection) add(candidate outlineCandidate) error {
	if _, ok := budget.selected[candidate.path]; ok {
		return nil
	}
	file, ok := budget.index.byPath[candidate.path]
	if !ok || !outlineFileUsable(file) {
		return nil
	}
	entry := renderOutlineFile(file)
	newSize := outlineDocumentSize(
		len(budget.selected)+1,
		len(budget.index.files),
		budget.maxBytes,
		budget.entrySize+len(entry),
	)
	if newSize > budget.maxBytes {
		budget.rejectedByBudget[candidate.path] = true
		if candidate.required {
			return fmt.Errorf(
				"outline byte budget %d cannot fit required file %q (%d bytes after selection)",
				budget.maxBytes,
				candidate.path,
				newSize,
			)
		}
		return nil
	}
	budget.selected[candidate.path] = candidate.reason
	budget.entryBytes[candidate.path] = entry
	budget.entrySize += len(entry)
	return nil
}

func addReferencedOutlineCandidates(budget *outlineBudgetSelection, directPaths map[string]string) error {
	queue := sortedOutlinePaths(directPaths)
	visited := map[string]bool{}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if visited[current] {
			continue
		}
		visited[current] = true
		added, err := addOutlineReferencesFromFile(budget, current)
		if err != nil {
			return err
		}
		queue = append(queue, added...)
		sort.Strings(queue)
	}
	return nil
}

func addOutlineReferencesFromFile(budget *outlineBudgetSelection, current string) ([]string, error) {
	var added []string
	file := budget.index.byPath[current]
	names := sortedStringSet(outlineReferencedIdentifiers(file))
	for _, name := range names {
		declarations := bestOutlineDeclarations(current, budget.index.declarations[name])
		for _, declaration := range declarations {
			if _, ok := budget.selected[declaration]; ok {
				continue
			}
			candidate := outlineCandidate{path: declaration, reason: "declares referenced symbol " + name}
			if err := budget.add(candidate); err != nil {
				return nil, err
			}
			if _, ok := budget.selected[declaration]; ok {
				added = append(added, declaration)
			}
		}
	}
	return added, nil
}

func (budget *outlineBudgetSelection) result() (*outlineSelection, error) {
	selection := &outlineSelection{BudgetBytes: budget.maxBytes}
	for _, file := range budget.index.files {
		if reason, ok := budget.selected[file.Path]; ok {
			selection.Included = append(selection.Included, outlineFileDecision{Path: file.Path, Reason: reason})
			continue
		}
		selection.Omitted = append(selection.Omitted, outlineFileDecision{
			Path: file.Path, Reason: budget.omissionReason(file),
		})
	}
	selection.contents = renderSelectedOutline(budget.entryBytes, len(budget.index.files), budget.maxBytes)
	selection.RenderedBytes = len(selection.contents)
	if selection.RenderedBytes > budget.maxBytes {
		return nil, fmt.Errorf(
			"outline rendering exceeded byte budget: %d > %d",
			selection.RenderedBytes,
			budget.maxBytes,
		)
	}
	return selection, nil
}

func (budget *outlineBudgetSelection) omissionReason(file outline.File) string {
	switch {
	case budget.rejectedByBudget[file.Path]:
		return "outline byte budget"
	case file.Skipped != "":
		return "source omitted by outline: " + file.Skipped
	case isTestPath(file.Path):
		return "test path"
	default:
		return "outside observed usage"
	}
}

func outlineFileUsable(file outline.File) bool {
	return file.Skipped == "" && strings.TrimSpace(file.Content) != "" && !isTestPath(file.Path)
}

func hasUsableOutlineFile(files []outline.File) bool {
	for _, file := range files {
		if outlineFileUsable(file) {
			return true
		}
	}
	return false
}

func addRequiredOutlineCandidate(candidates map[string]outlineCandidate, path, reason string) {
	if _, ok := candidates[path]; !ok {
		candidates[path] = outlineCandidate{path: path, reason: reason, required: true}
	}
}

func sortedOutlineCandidates(candidates map[string]outlineCandidate, required bool) []outlineCandidate {
	result := make([]outlineCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.required == required {
			result = append(result, candidate)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].path < result[j].path })
	return result
}

func outlineUsageIdentifiers(surface *usage.Surface) map[string]bool {
	result := map[string]bool{}
	if surface == nil {
		return result
	}
	for _, symbol := range surface.Symbols {
		for _, name := range outlineIdentifierList(symbol.Name) {
			result[name] = true
		}
	}
	return result
}

func outlineModuleNames(surface *usage.Surface) map[string]bool {
	result := map[string]bool{}
	if surface == nil {
		return result
	}
	depName := strings.TrimRight(surface.Dep, "/")
	if separator := strings.LastIndex(depName, "/"); separator >= 0 {
		depName = depName[separator+1:]
	}
	if depName != "" {
		result[normalizeOutlineName(depName)] = true
	}
	for _, symbol := range surface.Symbols {
		parts := outlineIdentifierList(symbol.Name)
		if len(parts) > 1 {
			parts = parts[:len(parts)-1]
		} else if symbol.Kind == "named" || symbol.Kind == "member" {
			parts = nil
		}
		for _, name := range parts {
			result[normalizeOutlineName(name)] = true
		}
	}
	delete(result, "")
	return result
}

func outlineIdentifiers(value string) map[string]bool {
	result := map[string]bool{}
	for _, name := range outlineIdentifierList(value) {
		result[name] = true
	}
	return result
}

func outlineIdentifierList(value string) []string {
	var result []string
	start := -1
	for i, r := range value {
		if unicode.IsLetter(r) || r == '_' || start >= 0 && unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			result = append(result, value[start:i])
			start = -1
		}
	}
	if start >= 0 {
		result = append(result, value[start:])
	}
	return result
}

func sortedStringSet(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func normalizeOutlineName(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func outlinePathMatchesNames(path string, names map[string]bool) bool {
	dir := pathpkg.Base(pathpkg.Dir(path))
	return names[normalizeOutlineName(dir)]
}

func outlineStemMatchesNames(path string, names map[string]bool) bool {
	base := pathpkg.Base(path)
	stem := strings.TrimSuffix(base, pathpkg.Ext(base))
	return names[normalizeOutlineName(stem)] && !outlineEntryNames[strings.ToLower(base)]
}

func outlineContextCandidates(files []outline.File, fallback bool) []outlineCandidate {
	var entries, contextFiles, exported []outlineCandidate
	for _, file := range files {
		if !outlineFileUsable(file) {
			continue
		}
		base := strings.ToLower(pathpkg.Base(file.Path))
		depth := strings.Count(file.Path, "/")
		if fallback && outlineEntryNames[base] && depth <= 2 {
			entries = append(entries, outlineCandidate{path: file.Path, reason: "package entry point fallback"})
		}
		if depth == 0 && (outlineContextNames[base] || strings.HasPrefix(base, "readme.") || strings.HasSuffix(base, ".gemspec")) {
			contextFiles = append(contextFiles, outlineCandidate{path: file.Path, reason: "package context"})
		}
		if fallback && len(file.Symbols) > 0 {
			for _, symbol := range file.Symbols {
				if symbol.Exported {
					exported = append(exported, outlineCandidate{path: file.Path, reason: "exported source fallback"})
					break
				}
			}
		}
	}
	result := make([]outlineCandidate, 0, len(entries)+len(contextFiles)+len(exported))
	result = append(result, entries...)
	result = append(result, contextFiles...)
	if fallback {
		result = append(result, exported...)
	}
	seen := map[string]bool{}
	unique := result[:0]
	for _, candidate := range result {
		if !seen[candidate.path] {
			seen[candidate.path] = true
			unique = append(unique, candidate)
		}
	}
	return unique
}

func outlineReferencedIdentifiers(file outline.File) map[string]bool {
	result := map[string]bool{}
	imports, _ := outline.Imports([]byte(file.Content), file.Path)
	for _, imported := range imports {
		for _, name := range imported.Names {
			if name.Name != "" {
				result[name.Name] = true
			}
		}
	}
	for name := range outlineIdentifiers(file.Content) {
		first, _ := utf8.DecodeRuneInString(name)
		if unicode.IsUpper(first) {
			result[name] = true
		}
	}
	return result
}

func bestOutlineDeclarations(current string, declarations []string) []string {
	anchor := outlineSourceAnchor(current)
	bestScore := -1
	var best []string
	for _, candidate := range declarations {
		if outlineAuxiliaryPath(candidate) {
			continue
		}
		score := commonOutlinePathSegments(pathpkg.Dir(current), pathpkg.Dir(candidate))
		if outlineSourceAnchor(candidate) == anchor {
			score += 1000
		}
		switch {
		case score > bestScore:
			bestScore = score
			best = []string{candidate}
		case score == bestScore:
			best = append(best, candidate)
		}
	}
	return best
}

func outlineSourceAnchor(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) == 1 {
		return "."
	}
	if (parts[0] == "src" || parts[0] == "packages") && len(parts) > 2 {
		return strings.Join(parts[:2], "/")
	}
	return parts[0]
}

func outlineAuxiliaryPath(path string) bool {
	parts := strings.Split(strings.ToLower(path), "/")
	for _, part := range parts[:len(parts)-1] {
		switch part {
		case ".ci", "ci", "doc", "docs", "example", "examples", "benchmark", "benchmarks":
			return true
		}
	}
	switch pathpkg.Base(strings.ToLower(path)) {
	case "noxfile.py", "setup.py":
		return true
	}
	return false
}

func commonOutlinePathSegments(left, right string) int {
	leftParts := strings.Split(left, "/")
	rightParts := strings.Split(right, "/")
	count := 0
	for count < len(leftParts) && count < len(rightParts) && leftParts[count] == rightParts[count] {
		count++
	}
	return count
}

func sortedOutlinePaths(selected map[string]string) []string {
	paths := make([]string, 0, len(selected))
	for path := range selected {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func renderOutlineFile(file outline.File) []byte {
	var b bytes.Buffer
	fence := outlineFence(file.Content)
	fmt.Fprintf(&b, "### %s\n\n%s%s\n", file.Path, fence, file.Language)
	b.WriteString(strings.TrimRight(file.Content, "\n"))
	fmt.Fprintf(&b, "\n%s\n\n", fence)
	return b.Bytes()
}

func outlineFence(content string) string {
	longest := 2
	current := 0
	for _, r := range content {
		if r == '`' {
			current++
			if current > longest {
				longest = current
			}
		} else {
			current = 0
		}
	}
	return strings.Repeat("`", longest+1)
}

func outlineDocumentSize(selected, total, budget, entries int) int {
	return len(outlineDocumentHeader(selected, total, budget)) + entries
}

func outlineDocumentHeader(selected, total, budget int) []byte {
	omitted := total - selected
	return fmt.Appendf(nil,
		"_Selected %d of %d files within the %d-byte outline budget; %d omitted. See `%s` for details._\n\n## Files\n\n",
		selected, total, budget, omitted, outlineSelectionFile,
	)
}

func renderSelectedOutline(entries map[string][]byte, total, budget int) []byte {
	paths := make([]string, 0, len(entries))
	for path := range entries {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var b bytes.Buffer
	b.Write(outlineDocumentHeader(len(paths), total, budget))
	for _, path := range paths {
		b.Write(entries[path])
	}
	return b.Bytes()
}
