package main

import (
	"strings"
	"testing"
)

func TestUsageShowsAcceptedArgumentOrder(t *testing.T) {
	for _, want := range []string{
		"hyrum surface [--config file] [--dep name] [--symbol name] [--scope scope] [--include path] [--exclude path] [--json] [path]",
		"hyrum gen     [--config file] [--dep name] [--symbol name] [--batch-size N] [--batch-sites N] [--outline-bytes N] [--scope scope] [--include path] [--exclude path] [--out dir] [--work dir] [--backend name] [--run] [path]",
		"hyrum check   --dep name[@version] [--tests dir] [path]",
		"hyrum corpus  --upstream purl --out dir [--dependent URL[@ref]] [--discover N] [--outline-bytes N]",
	} {
		if !strings.Contains(usageText, want) {
			t.Errorf("usage does not contain %q", want)
		}
	}
	for _, stale := range []string{"surface <path>", "gen     <path>", "--dependents"} {
		if strings.Contains(usageText, stale) {
			t.Errorf("usage still contains %q", stale)
		}
	}
}
