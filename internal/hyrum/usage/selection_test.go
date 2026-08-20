package usage

import (
	"reflect"
	"testing"
)

func TestSelectSymbolsUsesExactNamesAndReportsMissing(t *testing.T) {
	surface := &Surface{
		Dep: "dep",
		Symbols: []Symbol{
			{Name: "zeta", Sites: []Site{{File: "z.go", Line: 3}}},
			{Name: "alpha", Sites: []Site{{File: "a.go", Line: 1}}},
			{Name: "alpha.member", Sites: []Site{{File: "a.go", Line: 2}}},
		},
	}
	selected, missing := SelectSymbols(surface, []string{"alpha.member", "alpha", "absent", "absent"})
	if got := surfaceSymbolNames(selected); !reflect.DeepEqual(got, []string{"alpha", "alpha.member"}) {
		t.Fatalf("selected symbols = %v", got)
	}
	if !reflect.DeepEqual(missing, []string{"absent"}) {
		t.Fatalf("missing symbols = %v", missing)
	}
	if len(surface.Symbols) != 3 || surface.Symbols[0].Name != "zeta" {
		t.Fatalf("source surface changed: %+v", surface.Symbols)
	}
}

func TestPartitionSymbolsIsStableAndCopiesSites(t *testing.T) {
	surface := &Surface{Symbols: []Symbol{
		{Name: "zeta", Sites: []Site{{File: "z.go"}}},
		{Name: "alpha", Sites: []Site{{File: "a.go"}}},
		{Name: "middle", Sites: []Site{{File: "m.go"}}},
	}}
	batches := PartitionSurface(surface, 2, 0)
	if len(batches) != 2 {
		t.Fatalf("batches = %d", len(batches))
	}
	if got := surfaceSymbolNames(batches[0]); !reflect.DeepEqual(got, []string{"alpha", "middle"}) {
		t.Errorf("batch 1 = %v", got)
	}
	if got := surfaceSymbolNames(batches[1]); !reflect.DeepEqual(got, []string{"zeta"}) {
		t.Errorf("batch 2 = %v", got)
	}
	batches[0].Symbols[0].Sites[0].File = "changed.go"
	if surface.Symbols[1].Sites[0].File != "a.go" {
		t.Fatalf("partition shares site storage with source: %+v", surface.Symbols)
	}
}

func TestPartitionSymbolsKeepsOneEmptyBatch(t *testing.T) {
	batches := PartitionSurface(&Surface{Dep: "dep"}, 10, 0)
	if len(batches) != 1 || batches[0].Dep != "dep" || len(batches[0].Symbols) != 0 {
		t.Fatalf("empty batches = %+v", batches)
	}
}

func TestPartitionSurfaceSplitsLargeSymbolBySites(t *testing.T) {
	surface := &Surface{Symbols: []Symbol{
		{Name: "small", Sites: []Site{{File: "b.go", Line: 2}}},
		{Name: "large", Sites: []Site{
			{File: "c.go", Line: 3},
			{File: "a.go", Line: 1},
			{File: "b.go", Line: 2},
			{File: "d.go", Line: 4},
		}},
	}}
	batches := PartitionSurface(surface, 2, 3)
	if len(batches) != 2 {
		t.Fatalf("batches = %+v", batches)
	}
	if got := len(batches[0].Symbols[0].Sites); got != 3 || batches[0].Symbols[0].Name != "large" {
		t.Fatalf("batch 1 = %+v", batches[0].Symbols)
	}
	if batches[0].Symbols[0].Sites[0].File != "a.go" {
		t.Fatalf("sites are not sorted: %+v", batches[0].Symbols[0].Sites)
	}
	if got := surfaceSiteCount(batches[1]); got != 2 {
		t.Fatalf("batch 2 sites = %d, want 2: %+v", got, batches[1].Symbols)
	}
	if batches[1].Symbols[0].Name != "large" || batches[1].Symbols[1].Name != "small" {
		t.Fatalf("split symbol order = %+v", batches[1].Symbols)
	}
}

func surfaceSymbolNames(surface *Surface) []string {
	names := make([]string, 0, len(surface.Symbols))
	for _, symbol := range surface.Symbols {
		names = append(names, symbol.Name)
	}
	return names
}

func surfaceSiteCount(surface *Surface) int {
	count := 0
	for _, symbol := range surface.Symbols {
		count += len(symbol.Sites)
	}
	return count
}
