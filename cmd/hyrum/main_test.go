package main

import (
	"strings"
	"testing"
)

func TestUsageShowsAcceptedArgumentOrder(t *testing.T) {
	for _, want := range []string{
		"hyrum surface [--dep name] [--json] [path]",
		"hyrum gen     [--dep name] [--out dir] [--backend name] [--run] [path]",
		"hyrum check   --dep name[@version] [--tests dir] [path]",
		"hyrum corpus  --upstream purl --out dir [--dependent URL[@ref]] [--discover N]",
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
