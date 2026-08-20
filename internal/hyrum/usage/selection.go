package usage

import "sort"

// SelectSymbols returns a copy of surface containing exact symbol-name
// matches. The result is sorted by name so repeated runs produce the same
// ordering. Missing names are returned once in request order.
func SelectSymbols(surface *Surface, names []string) (*Surface, []string) {
	if surface == nil {
		return nil, append([]string(nil), names...)
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	selected := make([]Symbol, 0, len(wanted))
	for _, symbol := range surface.Symbols {
		if wanted[symbol.Name] {
			selected = append(selected, cloneSymbol(symbol))
			delete(wanted, symbol.Name)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })

	seenMissing := map[string]bool{}
	var missing []string
	for _, name := range names {
		if wanted[name] && !seenMissing[name] {
			seenMissing[name] = true
			missing = append(missing, name)
		}
	}
	return cloneSurfaceWithSymbols(surface, selected), missing
}

// PartitionSurface splits a surface by sorted symbol name and site order.
// Positive limits cap the number of symbol entries and sites in each batch.
// A symbol with more sites than the site limit is split across batches. An
// empty surface still produces one batch for history-derived contracts.
func PartitionSurface(surface *Surface, maxSymbols, maxSites int) []*Surface {
	if surface == nil {
		return nil
	}
	symbols := sortedSurfaceSymbols(surface.Symbols)
	if maxSymbols <= 0 && maxSites <= 0 {
		return []*Surface{cloneSurfaceWithSymbols(surface, symbols)}
	}
	batches := partitionSortedSymbols(surface, symbols, maxSymbols, maxSites)
	if len(batches) == 0 {
		return []*Surface{cloneSurfaceWithSymbols(surface, nil)}
	}
	return batches
}

func sortedSurfaceSymbols(source []Symbol) []Symbol {
	symbols := make([]Symbol, len(source))
	for i, symbol := range source {
		symbols[i] = cloneSymbol(symbol)
		sort.Slice(symbols[i].Sites, func(a, b int) bool {
			left, right := symbols[i].Sites[a], symbols[i].Sites[b]
			if left.File != right.File {
				return left.File < right.File
			}
			if left.Line != right.Line {
				return left.Line < right.Line
			}
			if left.Scope != right.Scope {
				return left.Scope < right.Scope
			}
			return left.Context < right.Context
		})
	}
	sort.Slice(symbols, func(i, j int) bool { return symbols[i].Name < symbols[j].Name })
	return symbols
}

func partitionSortedSymbols(surface *Surface, symbols []Symbol, maxSymbols, maxSites int) []*Surface {
	var batches []*Surface
	var current []Symbol
	currentSites := 0
	flush := func() {
		if len(current) == 0 {
			return
		}
		batches = append(batches, cloneSurfaceWithSymbols(surface, current))
		current = nil
		currentSites = 0
	}
	for _, symbol := range symbols {
		if len(symbol.Sites) == 0 {
			if maxSymbols > 0 && len(current) >= maxSymbols {
				flush()
			}
			current = append(current, symbol)
			continue
		}
		remaining := symbol.Sites
		for len(remaining) > 0 {
			if batchLimitReached(len(current), currentSites, maxSymbols, maxSites) {
				flush()
			}
			take := len(remaining)
			if maxSites > 0 {
				take = min(take, maxSites-currentSites)
			}
			fragment := symbol
			fragment.Sites = append([]Site(nil), remaining[:take]...)
			current = append(current, fragment)
			currentSites += take
			remaining = remaining[take:]
		}
	}
	flush()
	return batches
}

func batchLimitReached(symbols, sites, maxSymbols, maxSites int) bool {
	return maxSymbols > 0 && symbols >= maxSymbols || maxSites > 0 && sites >= maxSites
}

func cloneSurfaceWithSymbols(surface *Surface, symbols []Symbol) *Surface {
	clone := *surface
	clone.Symbols = make([]Symbol, len(symbols))
	for i, symbol := range symbols {
		clone.Symbols[i] = cloneSymbol(symbol)
	}
	return &clone
}

func cloneSymbol(symbol Symbol) Symbol {
	symbol.Sites = append([]Site(nil), symbol.Sites...)
	return symbol
}
