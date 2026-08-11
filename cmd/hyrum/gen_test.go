package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alpha-omega-security/harness"
	"github.com/alpha-omega-security/hyrum/internal/hyrum"
)

func TestAnyRan(t *testing.T) {
	if anyRan(nil) {
		t.Error("nil slice")
	}
	if anyRan([]hyrum.VerifyResult{{Error: "install failed"}}) {
		t.Error("error-only should not count as ran")
	}
	if !anyRan([]hyrum.VerifyResult{{Version: "1.0", Pass: 3}}) {
		t.Error("pass counts as ran")
	}
	if !anyRan([]hyrum.VerifyResult{{Error: "x"}, {Version: "2.0", Fail: 1}}) {
		t.Error("mixed: one ran")
	}
}

func TestAddBackendRecoveries(t *testing.T) {
	meta := map[string]any{}
	addBackendRecoveries(meta, nil)
	if len(meta) != 0 {
		t.Fatalf("clean run changed metadata: %v", meta)
	}

	addBackendRecoveries(meta, []string{"hyrum-usage", "hyrum-validate"})
	if got := meta["recovered_output"]; got != true {
		t.Errorf("recovered_output = %v", got)
	}
	steps, ok := meta["recovered_steps"].([]string)
	if !ok || len(steps) != 2 || steps[0] != "hyrum-usage" || steps[1] != "hyrum-validate" {
		t.Errorf("recovered_steps = %v", meta["recovered_steps"])
	}
	if _, ok := meta["backend_error"]; ok {
		t.Error("raw backend error must not be persisted")
	}
}

type recoveredRunner struct {
	output json.RawMessage
}

func (r recoveredRunner) RunSkill(context.Context, harness.Harness, string, string, string) (*hyrum.RunResult, error) {
	output := r.output
	if output == nil {
		output = json.RawMessage(`{"verdicts":[{"test":"t","status":"weak","action":"strengthen","reasoning":"r"}]}`)
	}
	return &hyrum.RunResult{
		Output:       output,
		CostUSD:      0.25,
		BackendError: "backend exited non-zero after writing fresh output",
	}, nil
}

func TestRunValidateReturnsRecovery(t *testing.T) {
	p := &pipeline{h: harness.ClaudeHarness{}}
	out, cost, recovery, err := p.runValidate(context.Background(), recoveredRunner{}, t.TempDir(), []hyrum.VerifyResult{{Pass: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || len(out.Verdicts) != 1 {
		t.Fatalf("validate output = %+v", out)
	}
	if cost != 0.25 {
		t.Errorf("cost = %v", cost)
	}
	if recovery == "" {
		t.Fatal("validate recovery was discarded")
	}
}

func TestRunValidateDecodeFailureDoesNotReturnRecovery(t *testing.T) {
	p := &pipeline{h: harness.ClaudeHarness{}}
	out, _, recovery, err := p.runValidate(
		context.Background(),
		recoveredRunner{output: json.RawMessage(`{"verdicts":"bad"}`)},
		t.TempDir(),
		[]hyrum.VerifyResult{{Pass: 1}},
	)
	if err == nil {
		t.Fatal("want decode error")
	}
	if out != nil {
		t.Fatalf("discarded validation output = %+v", out)
	}
	if recovery != "" {
		t.Fatalf("discarded output reported as recovered: %q", recovery)
	}
}
