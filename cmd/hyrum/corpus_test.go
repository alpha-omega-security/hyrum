package main

import (
	"strings"
	"testing"
)

func TestCmdCorpusRejectsNonPositiveOutlineBudget(t *testing.T) {
	err := cmdCorpus(t.Context(), []string{
		"--upstream", "pkg:npm/ws",
		"--out", t.TempDir(),
		"--outline-bytes", "0",
	})
	if err == nil || !strings.Contains(err.Error(), "--outline-bytes must be greater than zero") {
		t.Fatalf("error = %v", err)
	}
}

func TestCmdCorpusReturnsAllDependentFailures(t *testing.T) {
	first := "file:///missing-first"
	second := "file:///missing-second"
	err := cmdCorpus(t.Context(), []string{
		"--upstream", "pkg:npm/example",
		"--out", t.TempDir(),
		"--work", t.TempDir(),
		"--dependent", first,
		"--dependent", second,
	})
	if err == nil {
		t.Fatal("cmdCorpus discarded dependent failures")
	}
	for _, want := range []string{first, second} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("cmdCorpus error %q does not include %q", err, want)
		}
	}
}

func TestCmdCorpusRedactsDependentCredentialsInErrors(t *testing.T) {
	const spec = "https://secret@example.com/owner/repo@bad ref"
	err := cmdCorpus(t.Context(), []string{
		"--upstream", "pkg:npm/example",
		"--out", t.TempDir(),
		"--work", t.TempDir(),
		"--dependent", spec,
	})
	if err == nil {
		t.Fatal("cmdCorpus accepted an invalid dependent ref")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("cmdCorpus error exposed URL credentials: %q", err)
	}
	if !strings.Contains(err.Error(), "REDACTED") {
		t.Fatalf("cmdCorpus error did not identify the redacted dependent: %q", err)
	}
}
