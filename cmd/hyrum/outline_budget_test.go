package main

import (
	"strings"
	"testing"

	"github.com/alpha-omega-security/hyrum/internal/hyrum/usage"
	"github.com/git-pkgs/outline"
)

func TestSelectDependencyOutlinePrioritizesUsedEntriesAndReferences(t *testing.T) {
	packed := &outline.Result{Files: []outline.File{
		{Path: "examples/types.py", Content: "class Options:\n    pass\n", Language: "python", Symbols: []outline.Symbol{{Name: "Options", Exported: true}}},
		{Path: "src/pkg/unrelated.py", Content: "class Unrelated:\n    pass\n", Language: "python", Symbols: []outline.Symbol{{Name: "Unrelated", Exported: true}}},
		{Path: "src/pkg/types.py", Content: "class Options:\n    pass\n", Language: "python", Symbols: []outline.Symbol{{Name: "Options", Exported: true}}},
		{Path: "src/pkg/client.py", Content: "class Client:\n    def open(self, options: Options):\n        pass\n", Language: "python", Symbols: []outline.Symbol{{Name: "Client", Exported: true}}},
		{Path: "src/pkg/__init__.py", Content: "from .async_client import AsyncClient\nfrom .client import Client\n", Language: "python"},
		{Path: "src/pkg/async_client.py", Content: "class AsyncClient:\n    pass\n", Language: "python", Symbols: []outline.Symbol{{Name: "AsyncClient", Exported: true}}},
		{Path: "tests/test_client.py", Content: "class Client:\n    pass\n", Language: "python", Symbols: []outline.Symbol{{Name: "Client", Exported: true}}},
	}}
	surface := &usage.Surface{Dep: "pkg", Symbols: []usage.Symbol{{Name: "pkg.Client", Kind: "member"}}}

	selection, err := selectDependencyOutline(packed, surface, 8<<10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"src/pkg/__init__.py", "src/pkg/client.py", "src/pkg/types.py"}
	if got := decisionPaths(selection.Included); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("included = %v, want %v", got, want)
	}
	if strings.Contains(string(selection.contents), "Unrelated") || strings.Contains(string(selection.contents), "tests/test_client.py") {
		t.Fatalf("outline contains unrelated source:\n%s", selection.contents)
	}
	if selection.RenderedBytes != len(selection.contents) || selection.RenderedBytes > selection.BudgetBytes {
		t.Fatalf("rendered=%d contents=%d budget=%d", selection.RenderedBytes, len(selection.contents), selection.BudgetBytes)
	}
}

func TestBestOutlineDeclarationsPreferNearestSourceTree(t *testing.T) {
	declarations := []string{
		".ci/version.sh",
		"elasticsearch/_async/client/options.py",
		"elasticsearch/_sync/client/options.py",
		"examples/options.py",
	}
	got := bestOutlineDeclarations("elasticsearch/_sync/client/__init__.py", declarations)
	if len(got) != 1 || got[0] != "elasticsearch/_sync/client/options.py" {
		t.Fatalf("declarations = %v", got)
	}
}

func TestOutlineReferencedIdentifiersExcludeUnimportedLowercaseNames(t *testing.T) {
	file := outline.File{
		Path:    "client.py",
		Content: "from .types import Options\nclass Client:\n    logger = logger\n    option: Options\n",
	}
	got := outlineReferencedIdentifiers(file)
	if !got["Options"] || !got["Client"] {
		t.Fatalf("referenced identifiers = %v", got)
	}
	if got["logger"] {
		t.Fatalf("lowercase identifier treated as referenced declaration: %v", got)
	}
}

func TestSelectDependencyOutlineRejectsRequiredFileOverBudget(t *testing.T) {
	packed := &outline.Result{Files: []outline.File{{
		Path: "client.py", Content: "class Client:\n" + strings.Repeat("    value = 1\n", 100),
		Symbols: []outline.Symbol{{Name: "Client", Exported: true}},
	}}}
	surface := &usage.Surface{Symbols: []usage.Symbol{{Name: "Client"}}}

	_, err := selectDependencyOutline(packed, surface, 128)
	if err == nil || !strings.Contains(err.Error(), "cannot fit required file") || !strings.Contains(err.Error(), "client.py") {
		t.Fatalf("error = %v", err)
	}
}

func TestSelectDependencyOutlineRecordsReferencedFileRejectedByBudget(t *testing.T) {
	client := outline.File{
		Path: "client.py", Content: "class Client:\n    option: Options\n",
		Symbols: []outline.Symbol{{Name: "Client", Exported: true}},
	}
	options := outline.File{
		Path: "options.py", Content: "class Options:\n" + strings.Repeat("    value = 1\n", 100),
		Symbols: []outline.Symbol{{Name: "Options", Exported: true}},
	}
	packed := &outline.Result{Files: []outline.File{options, client}}
	surface := &usage.Surface{Symbols: []usage.Symbol{{Name: "Client"}}}
	budget := outlineDocumentSize(1, 2, 512, len(renderOutlineFile(client)))

	selection, err := selectDependencyOutline(packed, surface, budget)
	if err != nil {
		t.Fatal(err)
	}
	if got := decisionPaths(selection.Included); len(got) != 1 || got[0] != "client.py" {
		t.Fatalf("included = %v", got)
	}
	if reason := decisionReason(selection.Omitted, "options.py"); reason != "outline byte budget" {
		t.Fatalf("options omission = %q", reason)
	}
	if selection.RenderedBytes > budget {
		t.Fatalf("rendered %d bytes exceeds budget %d", selection.RenderedBytes, budget)
	}
}

func TestSelectDependencyOutlineUsesPackageFallbackWithoutUsageMatches(t *testing.T) {
	packed := &outline.Result{Files: []outline.File{
		{Path: "src/index.ts", Content: "export class Client {}\n", Language: "typescript", Symbols: []outline.Symbol{{Name: "Client", Exported: true}}},
		{Path: "src/other.ts", Content: "export class Other {}\n", Language: "typescript", Symbols: []outline.Symbol{{Name: "Other", Exported: true}}},
	}}

	selection, err := selectDependencyOutline(packed, &usage.Surface{Dep: "unknown"}, 4<<10)
	if err != nil {
		t.Fatal(err)
	}
	if reason := decisionReason(selection.Included, "src/index.ts"); reason != "package entry point fallback" {
		t.Fatalf("index reason = %q; included = %+v", reason, selection.Included)
	}
}

func TestSelectDependencyOutlineMatchesExactSymbolName(t *testing.T) {
	packed := &outline.Result{Files: []outline.File{
		{Path: "client.py", Content: "class Client:\n    pass\n", Symbols: []outline.Symbol{{Name: "Client", Exported: true}}},
		{Path: "not_client.py", Content: "class NotClient:\n    pass\n", Symbols: []outline.Symbol{{Name: "NotClient", Exported: true}}},
	}}
	selection, err := selectDependencyOutline(packed, &usage.Surface{Symbols: []usage.Symbol{{Name: "Client"}}}, 4<<10)
	if err != nil {
		t.Fatal(err)
	}
	if reason := decisionReason(selection.Included, "not_client.py"); reason != "" {
		t.Fatalf("substring declaration included: %q", reason)
	}
}

func TestOutlineModuleNamesPreserveQualifiedNameOrder(t *testing.T) {
	surface := &usage.Surface{
		Dep: "github.com/example/client-lib",
		Symbols: []usage.Symbol{
			{Name: "client.Client", Kind: "member"},
			{Name: "Options", Kind: "named"},
		},
	}
	got := outlineModuleNames(surface)
	for _, want := range []string{"clientlib", "client"} {
		if !got[want] {
			t.Fatalf("module names = %v, want %q", got, want)
		}
	}
	if got["options"] {
		t.Fatalf("named import treated as module: %v", got)
	}
}

func TestSelectDependencyOutlineDoesNotRequireRawFileMatchingModuleName(t *testing.T) {
	packed := &outline.Result{Files: []outline.File{
		{Path: "docs/client.md", Content: strings.Repeat("documentation\n", 100)},
		{Path: "src/client.py", Content: "class Client:\n    pass\n", Outlined: true, Symbols: []outline.Symbol{{Name: "Client", Exported: true}}},
	}}
	surface := &usage.Surface{Dep: "client", Symbols: []usage.Symbol{{Name: "Client", Kind: "named"}}}

	selection, err := selectDependencyOutline(packed, surface, 512)
	if err != nil {
		t.Fatal(err)
	}
	if reason := decisionReason(selection.Included, "docs/client.md"); reason != "" {
		t.Fatalf("raw module-named file required: %q", reason)
	}
}

func TestRenderOutlineFileUsesFenceLongerThanContents(t *testing.T) {
	rendered := string(renderOutlineFile(outline.File{Path: "README.md", Content: "````\nvalue\n````"}))
	if !strings.Contains(rendered, "`````\n") {
		t.Fatalf("rendered fence is not longer than content:\n%s", rendered)
	}
}

func decisionPaths(decisions []outlineFileDecision) []string {
	paths := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		paths = append(paths, decision.Path)
	}
	return paths
}

func decisionReason(decisions []outlineFileDecision, path string) string {
	for _, decision := range decisions {
		if decision.Path == path {
			return decision.Reason
		}
	}
	return ""
}
