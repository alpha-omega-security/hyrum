package hyrum

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/git-pkgs/managers"
)

type addErrorManager struct {
	managers.Manager
}

type addSuccessManager struct {
	managers.Manager
}

func (addSuccessManager) Name() string      { return "test" }
func (addSuccessManager) Ecosystem() string { return "test" }
func (addSuccessManager) Add(context.Context, string, managers.AddOptions) (*managers.Result, error) {
	return &managers.Result{}, nil
}

func (addErrorManager) Name() string      { return "test" }
func (addErrorManager) Ecosystem() string { return EcoNPM }
func (addErrorManager) Add(context.Context, string, managers.AddOptions) (*managers.Result, error) {
	return nil, errors.New("invalid package")
}

func TestVerifyOneHandlesAddErrorWithoutResult(t *testing.T) {
	result := verifyOne(t.Context(), addErrorManager{}, nil, t.TempDir(), "example", "1.0.0", nil)
	if !strings.Contains(result.Error, "invalid package") {
		t.Fatalf("verifyOne error = %q", result.Error)
	}
}

func TestVerifyOneDoesNotTrustForgedPassOnNonzeroExit(t *testing.T) {
	tc := func(string, []string) []string {
		return []string{os.Args[0], "-test.run=TestVerifyForgedPassHelper", "--", "forged-pass"}
	}
	result := verifyOne(t.Context(), addSuccessManager{}, tc, t.TempDir(), "example", "1.0.0", nil)
	if result.Pass != 1 {
		t.Fatalf("control output was not parsed: %+v", result)
	}
	if result.Error == "" {
		t.Fatalf("nonzero runner exit was reported as a pass: %+v", result)
	}
}

func TestVerifyForgedPassHelper(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "forged-pass" {
		return
	}
	fmt.Println("pass 1")
	os.Exit(1)
}

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
