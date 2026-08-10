package hyrum

import (
	"reflect"
	"testing"
)

func TestParseTestOutputNode(t *testing.T) {
	out := `✔ constructs and closes (1.0ms)
✖ delivers text message payloads as strings (1.2ms)
  AssertionError: ...
ℹ tests 7
ℹ pass 6
ℹ fail 1
`
	pass, fail, failed := parseTestOutput(out)
	if pass != 6 || fail != 1 {
		t.Errorf("pass=%d fail=%d", pass, fail)
	}
	if !reflect.DeepEqual(failed, []string{"delivers text message payloads as strings"}) {
		t.Errorf("failed=%v", failed)
	}
}

func TestParseTestOutputPytest(t *testing.T) {
	out := `FAILED tests/x.py::TestA::test_foo - AssertionError
FAILED tests/x.py::TestA::test_bar - ImportError
2 failed, 71 passed in 0.4s
`
	pass, fail, failed := parseTestOutput(out)
	if pass != 71 || fail != 2 {
		t.Errorf("pass=%d fail=%d", pass, fail)
	}
	if len(failed) != 2 {
		t.Errorf("failed=%v", failed)
	}
}

func TestParseTestOutputGo(t *testing.T) {
	out := `--- PASS: TestA (0.00s)
--- FAIL: TestB (0.00s)
--- PASS: TestC (0.00s)
FAIL
`
	pass, fail, failed := parseTestOutput(out)
	if pass != 2 || fail != 1 {
		t.Errorf("pass=%d fail=%d", pass, fail)
	}
	if !reflect.DeepEqual(failed, []string{"TestB"}) {
		t.Errorf("failed=%v", failed)
	}
}

func TestParseTestOutputUnknown(t *testing.T) {
	pass, fail, failed := parseTestOutput("some random output\n")
	if pass != 0 || fail != 0 || failed != nil {
		t.Errorf("unknown output should be zeros: %d %d %v", pass, fail, failed)
	}
}
