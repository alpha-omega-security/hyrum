package main

import (
	"context"
	"errors"
	"testing"

	"github.com/alpha-omega-security/hyrum/internal/hyrum"
)

func TestSurfaceSummaryReturnsContextCancellation(t *testing.T) {
	target := &hyrum.Target{
		Path: t.TempDir(),
		Deps: []hyrum.Dep{{
			Name:      "flask",
			PURL:      "pkg:pypi/flask",
			Ecosystem: hyrum.EcoPyPI,
			Direct:    true,
		}},
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := surfaceSummary(ctx, target, true, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("surfaceSummary error = %v, want context canceled", err)
	}
}
