package hyrum

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alpha-omega-security/harness"
	harnesscontainer "github.com/alpha-omega-security/harness/container"
)

type streamFileHarness struct {
	harness.Harness
	streamPath string
}

func (streamFileHarness) Binary() string { return "/bin/sh" }

func (h streamFileHarness) Args(j harness.Job) []string {
	return []string{"-c", `cat "$1"; printf '%s' '{"files":[]}' > "$2"`, "hyrum-test", h.streamPath, j.OutputFile}
}

func TestRunnersAccumulateResultCosts(t *testing.T) {
	tests := []struct {
		name      string
		backend   harness.Harness
		model     string
		stream    string
		wantCost  float64
		wantTurns int
	}{
		{
			name: "claude repeated cumulative total", backend: harness.ClaudeHarness{}, model: "claude-haiku-4-5",
			stream: strings.Join([]string{
				`{"type":"system","subtype":"init","session_id":"session-1"}`,
				`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_1","model":"claude-haiku-4-5-20251001","usage":{"input_tokens":18,"output_tokens":3}}}}`,
				`{"type":"stream_event","event":{"type":"message_delta","usage":{"output_tokens":297}}}`,
				`{"type":"result","total_cost_usd":0.04407415,"num_turns":2,"usage":{"input_tokens":18,"output_tokens":297,"cache_read_input_tokens":37822,"cache_creation_input_tokens":6161}}`,
				`{"type":"result","total_cost_usd":0.04407415,"num_turns":1,"usage":{"input_tokens":10,"output_tokens":60,"cache_read_input_tokens":22787,"cache_creation_input_tokens":1452}}`,
			}, "\n"),
			wantCost: 0.04407415, wantTurns: 3,
		},
		{
			name: "claude increasing cumulative total", backend: harness.ClaudeHarness{},
			stream: `{"type":"result","total_cost_usd":0.01,"num_turns":1}` + "\n" +
				`{"type":"result","total_cost_usd":0.015,"num_turns":2}`,
			wantCost: 0.015, wantTurns: 3,
		},
		{
			name: "codex estimates each turn", backend: harness.CodexHarness{}, model: "gpt-5.4",
			stream: `{"type":"turn.completed","usage":{"input_tokens":1000,"output_tokens":10}}` + "\n" +
				`{"type":"turn.completed","usage":{"input_tokens":2000,"output_tokens":20}}`,
			wantCost: harness.CostFromUsage("gpt-5.4", harness.Usage{InputTokens: 3000, OutputTokens: 30}), wantTurns: 2,
		},
		{
			name: "copilot usage events do not add to checkpoint", backend: harness.CopilotHarness{},
			stream: strings.Join([]string{
				`{"type":"assistant.usage","data":{"model":"claude-haiku-4.5","inputTokens":1000,"outputTokens":10}}`,
				`{"type":"assistant.turn_end","data":{}}`,
				`{"type":"assistant.usage","data":{"model":"claude-haiku-4.5","inputTokens":2000,"outputTokens":20}}`,
				`{"type":"assistant.turn_end","data":{}}`,
				`{"type":"session.usage_checkpoint","data":{"totalNanoAiu":1500000000}}`,
				`{"type":"result","sessionId":"session-1","exitCode":0}`,
			}, "\n"),
			wantCost: 0.015, wantTurns: 2,
		},
		{
			name: "opencode accumulates step costs", backend: harness.OpencodeHarness{},
			stream: `{"type":"step_finish","part":{"cost":0.01,"tokens":{"input":100,"output":10}}}` + "\n" +
				`{"type":"step_finish","part":{"cost":0.02,"tokens":{"input":200,"output":20}}}`,
			wantCost: 0.03, wantTurns: 2,
		},
	}
	for _, tc := range tests {
		for _, mode := range []string{"host", "container"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				ws := t.TempDir()
				streamPath := filepath.Join(t.TempDir(), "stream.jsonl")
				if err := os.WriteFile(streamPath, []byte(tc.stream+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				h := streamFileHarness{Harness: tc.backend, streamPath: streamPath}
				var runner Runner = HostRunner{}
				if mode == "container" {
					t.Setenv("HYRUM_TEST_STREAM", streamPath)
					t.Setenv("HYRUM_TEST_OUTPUT", filepath.Join(ws, "tests.json"))
					runtimePath := filepath.Join(t.TempDir(), "runtime")
					script := "#!/bin/sh\n" + `cat "$HYRUM_TEST_STREAM"
printf '%s' '{"files":[]}' > "$HYRUM_TEST_OUTPUT"
`
					if err := os.WriteFile(runtimePath, []byte(script), 0o755); err != nil {
						t.Fatal(err)
					}
					runner = ContainerRunner{Runtime: harnesscontainer.Runtime{Bin: runtimePath}, Image: "test-image", Emit: func(harness.Event) {}}
				}
				got, err := runner.RunSkill(t.Context(), h, ws, "hyrum-generate", "tests.json", RunOptions{Model: tc.model})
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("run cost=$%.8f turns=%d output=%s", got.CostUSD, got.Turns, got.Output)
				if math.Abs(got.CostUSD-tc.wantCost) > 1e-12 || got.Turns != tc.wantTurns {
					t.Errorf("cost/turns = %.8f/%d, want %.8f/%d", got.CostUSD, got.Turns, tc.wantCost, tc.wantTurns)
				}
				if string(got.Output) != `{"files":[]}` {
					t.Errorf("output = %s", got.Output)
				}
			})
		}
	}
}
