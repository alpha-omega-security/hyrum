package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/alpha-omega-security/harness"
	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/alpha-omega-security/hyrum/internal/hyrum/usage"
)

type batchingRunner struct {
	calls       []string
	usageInputs [][]string
	breaksSeen  []bool
	generated   int
}

func (r *batchingRunner) RunSkill(
	_ context.Context,
	_ harness.Harness,
	ws, name, _ string,
	_ hyrum.RunOptions,
) (*hyrum.RunResult, error) {
	r.calls = append(r.calls, name)
	switch name {
	case "hyrum-history":
		return &hyrum.RunResult{Output: json.RawMessage(`{"breaks":[]}`), CostUSD: 0.1}, nil
	case "hyrum-usage":
		surface, err := readUsageSurface(ws, "usage.json")
		if err != nil {
			return nil, err
		}
		r.usageInputs = append(r.usageInputs, usageSymbolNames(surface))
		output, err := json.Marshal(tracedSurface{
			Calls: []json.RawMessage{json.RawMessage(`{"member":"shared","sites":[]}`)},
			Notes: "traced",
		})
		if err != nil {
			return nil, err
		}
		return &hyrum.RunResult{Output: output, CostUSD: 0.2}, nil
	case "hyrum-generate":
		_, err := os.Stat(filepath.Join(ws, "breaks.json"))
		r.breaksSeen = append(r.breaksSeen, err == nil)
		r.generated++
		output, err := json.Marshal(hyrum.GenerateResult{
			Files: []hyrum.GeneratedFile{{Path: "contract.test.js", Content: "// batch\n"}},
			Notes: "generated",
		})
		if err != nil {
			return nil, err
		}
		return &hyrum.RunResult{
			Output: output, CostUSD: 0.3, SessionID: "session-" + batchDirectory(r.generated),
		}, nil
	default:
		return nil, nil
	}
}

func TestRunBatchedGenerateSkillsMergesOutputsAndMetadata(t *testing.T) {
	ws := t.TempDir()
	surface := &usage.Surface{Dep: "sqlalchemy", Symbols: []usage.Symbol{
		{Name: "zeta", Sites: []usage.Site{{File: "z.py"}}},
		{Name: "alpha", Sites: []usage.Site{{File: "a.py"}}},
		{Name: "middle", Sites: []usage.Site{{File: "m.py"}}},
	}}
	batches := usage.PartitionSurface(surface, 2, 0)
	if err := stageUsageBatches(ws, batches); err != nil {
		t.Fatal(err)
	}
	runner := &batchingRunner{}
	p := &pipeline{h: harness.ClaudeHarness{}}

	run, err := p.runBatchedGenerateSkills(t.Context(), runner, ws, "sqlalchemy", batches)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{"hyrum-history", "hyrum-usage", "hyrum-generate", "hyrum-usage", "hyrum-generate"}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", runner.calls, wantCalls)
	}
	wantInputs := [][]string{{"alpha", "middle"}, {"zeta"}}
	if !reflect.DeepEqual(runner.usageInputs, wantInputs) {
		t.Fatalf("usage inputs = %v, want %v", runner.usageInputs, wantInputs)
	}
	if !reflect.DeepEqual(runner.breaksSeen, []bool{true, false}) {
		t.Fatalf("breaks visibility = %v", runner.breaksSeen)
	}
	if len(run.Generate.Files) != 2 || run.Generate.Files[0].Path != "batch-001/contract.test.js" || run.Generate.Files[1].Path != "batch-002/contract.test.js" {
		t.Fatalf("generated files = %+v", run.Generate.Files)
	}
	if len(run.Batches) != 2 || !reflect.DeepEqual(run.Batches[0].Symbols, []string{"alpha", "middle"}) || run.Batches[0].Sites != 2 {
		t.Fatalf("batch metadata = %+v", run.Batches)
	}
	if run.TotalCost < 1.09 || run.TotalCost > 1.11 || run.HistoryCost != 0.1 {
		t.Fatalf("costs = total %v, history %v", run.TotalCost, run.HistoryCost)
	}

	staged, err := readUsageSurface(ws, "usage.json")
	if err != nil {
		t.Fatal(err)
	}
	if got := usageSymbolNames(staged); !reflect.DeepEqual(got, []string{"alpha", "middle", "zeta"}) {
		t.Fatalf("restored usage = %v", got)
	}
	contents, err := os.ReadFile(filepath.Join(ws, "surface.json"))
	if err != nil {
		t.Fatal(err)
	}
	var traced tracedSurface
	if err := json.Unmarshal(contents, &traced); err != nil {
		t.Fatal(err)
	}
	if len(traced.Calls) != 1 {
		t.Fatalf("merged calls = %s", contents)
	}
	for _, path := range []string{
		filepath.Join(ws, "batches", "batch-001", "usage.json"),
		filepath.Join(ws, "batches", "batch-001", "surface.json"),
		filepath.Join(ws, "batches", "batch-001", "tests.json"),
		filepath.Join(ws, "batches", "batch-002", "tests.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("batch artifact %s: %v", path, err)
		}
	}

	meta := generationMeta("target", hyrum.Dep{Name: "sqlalchemy"}, stagedDependency{}, run.LastResult, run.Generate, run.TotalCost)
	addBatchMetadata(meta, 2, 500, run)
	if _, ok := meta["session_id"]; ok {
		t.Fatalf("top-level session retained for a multi-session run: %v", meta)
	}
	if meta["batch_count"] != 2 || meta["batch_size"] != 2 || meta["batch_sites"] != 500 {
		t.Fatalf("merged metadata = %v", meta)
	}
}

func TestMergeUsageBatchesReassemblesSplitSymbol(t *testing.T) {
	surface := &usage.Surface{Symbols: []usage.Symbol{{
		Name: "Session", Sites: []usage.Site{
			{File: "d.py", Line: 4}, {File: "b.py", Line: 2},
			{File: "c.py", Line: 3}, {File: "a.py", Line: 1},
		},
	}}}
	batches := usage.PartitionSurface(surface, 0, 2)
	merged := mergeUsageBatches(batches)
	if len(batches) != 2 || len(merged.Symbols) != 1 || len(merged.Symbols[0].Sites) != 4 {
		t.Fatalf("merged usage = %+v from %+v", merged.Symbols, batches)
	}
	if merged.Symbols[0].Sites[0].File != "a.py" || merged.Symbols[0].Sites[3].File != "d.py" {
		t.Fatalf("merged site order = %+v", merged.Symbols[0].Sites)
	}
}

func TestPrefixBatchFilesRejectsUnsafePathsBeforeJoining(t *testing.T) {
	for _, path := range []string{"../escape.js", "/absolute.js", "."} {
		if _, err := prefixBatchFiles([]hyrum.GeneratedFile{{Path: path}}, 1, 2); err == nil {
			t.Errorf("path %q accepted", path)
		}
	}
}
