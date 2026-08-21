package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alpha-omega-security/hyrum/internal/hyrum"
	"github.com/alpha-omega-security/hyrum/internal/hyrum/usage"
)

type generationBatchMetadata struct {
	Number         int      `json:"number"`
	Symbols        []string `json:"symbols"`
	Sites          int      `json:"sites"`
	SessionID      string   `json:"session_id,omitempty"`
	CostUSD        float64  `json:"cost_usd"`
	Notes          string   `json:"notes,omitempty"`
	RecoveredSteps []string `json:"recovered_steps,omitempty"`
}

type batchedGeneration struct {
	Generate       hyrum.GenerateResult
	LastResult     *hyrum.RunResult
	TotalCost      float64
	HistoryCost    float64
	RecoveredSteps []string
	Batches        []generationBatchMetadata
}

type completedGenerationBatch struct {
	Metadata       generationBatchMetadata
	Generate       hyrum.GenerateResult
	Result         *hyrum.RunResult
	Trace          json.RawMessage
	RecoveredSteps []string
}

type tracedSurface struct {
	EntryPoints []json.RawMessage `json:"entry_points,omitempty"`
	Calls       []json.RawMessage `json:"calls"`
	Notes       string            `json:"notes,omitempty"`
}

func readUsageSurface(path string) (*usage.Surface, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var surface usage.Surface
	if err := json.Unmarshal(contents, &surface); err != nil {
		return nil, err
	}
	return &surface, nil
}

func stageUsageBatches(ws string, batches []*usage.Surface) error {
	for i, batch := range batches {
		dir := filepath.Join(ws, "batches", batchDirectory(i+1))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := writeJSON(filepath.Join(dir, "usage.json"), batch); err != nil {
			return err
		}
	}
	return nil
}

func batchDirectory(number int) string {
	return "batch-" + fmt.Sprintf("%03d", number)
}

func (p *pipeline) runBatchedGenerateSkills(
	ctx context.Context,
	runner hyrum.Runner,
	ws, depName string,
	batches []*usage.Surface,
) (*batchedGeneration, error) {
	if len(batches) == 0 {
		return nil, fmt.Errorf("no usage batches")
	}

	run := &batchedGeneration{Batches: make([]generationBatchMetadata, 0, len(batches))}
	breaks, historyCost, historyRecoveries, err := p.runBatchHistory(ctx, runner, ws, depName)
	if err != nil {
		return nil, err
	}
	run.HistoryCost = historyCost
	run.TotalCost = historyCost
	run.RecoveredSteps = append(run.RecoveredSteps, historyRecoveries...)

	var traced []json.RawMessage
	var notes []string
	for i, batch := range batches {
		number := i + 1
		completed, err := p.runGenerationBatch(ctx, runner, ws, depName, batch, breaks, number, len(batches))
		if err != nil {
			return nil, err
		}
		run.TotalCost += completed.Metadata.CostUSD
		run.Batches = append(run.Batches, completed.Metadata)
		run.LastResult = completed.Result
		run.Generate.Files = append(run.Generate.Files, completed.Generate.Files...)
		run.RecoveredSteps = append(run.RecoveredSteps, completed.RecoveredSteps...)
		if completed.Trace != nil {
			traced = append(traced, completed.Trace)
		}
		if completed.Generate.Notes != "" {
			notes = append(notes, fmt.Sprintf("batch %d: %s", number, completed.Generate.Notes))
		}
	}

	run.Generate.Notes = strings.Join(notes, "\n")
	if err := restoreBatchedWorkspace(ws, batches, breaks, traced, run.Generate); err != nil {
		return nil, err
	}
	return run, nil
}

func (p *pipeline) runBatchHistory(
	ctx context.Context,
	runner hyrum.Runner,
	ws, depName string,
) ([]byte, float64, []string, error) {
	breaksPath := filepath.Join(ws, "breaks.json")
	_ = os.Remove(breaksPath)
	result, err := p.runGenerationStep(ctx, runner, ws, depName, skillSteps[1])
	if err != nil {
		return nil, 0, nil, nil
	}
	breaks := append([]byte(nil), result.Output...)
	if err := os.WriteFile(breaksPath, breaks, 0o644); err != nil {
		return nil, 0, nil, err
	}
	var recovered []string
	if result.BackendError != "" {
		recovered = append(recovered, skillSteps[1].name)
	}
	return breaks, result.CostUSD, recovered, nil
}

func (p *pipeline) runGenerationBatch(
	ctx context.Context,
	runner hyrum.Runner,
	ws, depName string,
	batch *usage.Surface,
	breaks []byte,
	number, total int,
) (*completedGenerationBatch, error) {
	fmt.Fprintf(os.Stderr, "  [batch %d/%d: %d symbol(s)]\n", number, total, len(batch.Symbols))
	if err := prepareGenerationBatch(ws, batch, breaks, number); err != nil {
		return nil, err
	}
	completed := &completedGenerationBatch{Metadata: generationBatchMetadata{
		Number: number, Symbols: usageSymbolNames(batch), Sites: usageSiteCount(batch),
	}}

	usageResult, usageErr := p.runGenerationStep(ctx, runner, ws, depName, skillSteps[0])
	if usageErr == nil {
		if err := completed.recordUsageResult(ws, usageResult); err != nil {
			return nil, err
		}
	}

	_ = os.Remove(filepath.Join(ws, "tests.json"))
	generateResult, err := p.runGenerationStep(ctx, runner, ws, depName, skillSteps[2])
	if err != nil {
		return nil, err
	}
	if err := completed.recordGenerateResult(ws, generateResult, number, total); err != nil {
		return nil, err
	}
	return completed, nil
}

func prepareGenerationBatch(ws string, batch *usage.Surface, breaks []byte, number int) error {
	if err := writeJSON(filepath.Join(ws, "usage.json"), batch); err != nil {
		return err
	}
	breaksPath := filepath.Join(ws, "breaks.json")
	if number == 1 && len(breaks) > 0 {
		return os.WriteFile(breaksPath, breaks, 0o644)
	}
	if err := os.Remove(breaksPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(filepath.Join(ws, "surface.json"))
	return nil
}

func (b *completedGenerationBatch) recordUsageResult(ws string, result *hyrum.RunResult) error {
	b.Metadata.CostUSD += result.CostUSD
	if result.BackendError != "" {
		b.Metadata.RecoveredSteps = append(b.Metadata.RecoveredSteps, skillSteps[0].name)
		b.RecoveredSteps = append(b.RecoveredSteps, skillSteps[0].name+" batch "+strconv.Itoa(b.Metadata.Number))
	}
	b.Trace = append(json.RawMessage(nil), result.Output...)
	if err := os.WriteFile(filepath.Join(ws, "surface.json"), result.Output, 0o644); err != nil {
		return err
	}
	return writeBatchRaw(ws, b.Metadata.Number, "surface.json", result.Output)
}

func (b *completedGenerationBatch) recordGenerateResult(
	ws string,
	result *hyrum.RunResult,
	number, total int,
) error {
	b.Metadata.CostUSD += result.CostUSD
	b.Metadata.SessionID = result.SessionID
	if result.BackendError != "" {
		b.Metadata.RecoveredSteps = append(b.Metadata.RecoveredSteps, skillSteps[2].name)
		b.RecoveredSteps = append(b.RecoveredSteps, skillSteps[2].name+" batch "+strconv.Itoa(number))
	}
	if err := result.Decode(&b.Generate); err != nil {
		return fmt.Errorf("decode tests.json batch %d: %w", number, err)
	}
	files, err := prefixBatchFiles(b.Generate.Files, number, total)
	if err != nil {
		return fmt.Errorf("batch %d: %w", number, err)
	}
	b.Generate.Files = files
	b.Metadata.Notes = b.Generate.Notes
	b.Result = result
	return writeBatchRaw(ws, number, "tests.json", result.Output)
}

func restoreBatchedWorkspace(
	ws string,
	batches []*usage.Surface,
	breaks []byte,
	traced []json.RawMessage,
	generated hyrum.GenerateResult,
) error {
	if err := writeJSON(filepath.Join(ws, "usage.json"), mergeUsageBatches(batches)); err != nil {
		return err
	}
	if len(breaks) > 0 {
		if err := os.WriteFile(filepath.Join(ws, "breaks.json"), breaks, 0o644); err != nil {
			return err
		}
	}
	if err := restoreMergedTrace(ws, traced); err != nil {
		return err
	}
	return writeJSON(filepath.Join(ws, "tests.json"), generated)
}

func restoreMergedTrace(ws string, traced []json.RawMessage) error {
	surface, err := mergeTracedSurfaces(traced)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  merge surface.json: %v (validation will use usage.json)\n", err)
		_ = os.Remove(filepath.Join(ws, "surface.json"))
		return nil
	}
	if surface == nil {
		return nil
	}
	return writeJSON(filepath.Join(ws, "surface.json"), surface)
}

func writeBatchRaw(ws string, number int, name string, contents []byte) error {
	return os.WriteFile(filepath.Join(ws, "batches", batchDirectory(number), name), contents, 0o644)
}

func usageSymbolNames(surface *usage.Surface) []string {
	names := make([]string, 0, len(surface.Symbols))
	for _, symbol := range surface.Symbols {
		names = append(names, symbol.Name)
	}
	return names
}

func usageSiteCount(surface *usage.Surface) int {
	count := 0
	for _, symbol := range surface.Symbols {
		count += len(symbol.Sites)
	}
	return count
}

func mergeUsageBatches(batches []*usage.Surface) *usage.Surface {
	merged := *batches[0]
	merged.Symbols = nil
	positions := map[string]int{}
	for _, batch := range batches {
		for _, symbol := range batch.Symbols {
			position, ok := positions[symbol.Name]
			if !ok {
				positions[symbol.Name] = len(merged.Symbols)
				symbol.Sites = append([]usage.Site(nil), symbol.Sites...)
				merged.Symbols = append(merged.Symbols, symbol)
				continue
			}
			merged.Symbols[position].Sites = append(merged.Symbols[position].Sites, symbol.Sites...)
		}
	}
	return &merged
}

func prefixBatchFiles(files []hyrum.GeneratedFile, number, total int) ([]hyrum.GeneratedFile, error) {
	prefixed := make([]hyrum.GeneratedFile, len(files))
	for i, file := range files {
		clean := filepath.Clean(file.Path)
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return nil, fmt.Errorf("refusing path %q", file.Path)
		}
		file.Path = clean
		if total > 1 {
			file.Path = filepath.Join(batchDirectory(number), clean)
		}
		prefixed[i] = file
	}
	return prefixed, nil
}

func mergeTracedSurfaces(outputs []json.RawMessage) (*tracedSurface, error) {
	if len(outputs) == 0 {
		return nil, nil
	}
	merged := &tracedSurface{Calls: []json.RawMessage{}}
	entrySeen := map[string]bool{}
	callSeen := map[string]bool{}
	var notes []string
	for i, output := range outputs {
		var part tracedSurface
		if err := json.Unmarshal(output, &part); err != nil {
			return nil, fmt.Errorf("batch %d: %w", i+1, err)
		}
		merged.EntryPoints = appendUniqueJSON(merged.EntryPoints, part.EntryPoints, entrySeen)
		merged.Calls = appendUniqueJSON(merged.Calls, part.Calls, callSeen)
		if part.Notes != "" {
			notes = append(notes, fmt.Sprintf("batch %d: %s", i+1, part.Notes))
		}
	}
	merged.Notes = strings.Join(notes, "\n")
	return merged, nil
}

func appendUniqueJSON(dst, values []json.RawMessage, seen map[string]bool) []json.RawMessage {
	for _, value := range values {
		key := string(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		dst = append(dst, value)
	}
	return dst
}

func addBatchMetadata(meta map[string]any, symbols, sites int, run *batchedGeneration) {
	delete(meta, "session_id")
	meta["batch_count"] = len(run.Batches)
	meta["history_cost_usd"] = run.HistoryCost
	meta["batches"] = run.Batches
	if symbols > 0 {
		meta["batch_size"] = symbols
	}
	if sites > 0 {
		meta["batch_sites"] = sites
	}
}
