package terminal

import "testing"

func TestSingleLineStripsTerminalControls(t *testing.T) {
	in := "safe\x1b[31mred\x1b[0m\x1b]0;forged title\x07\b\a\u202eTAIL\nforged line"
	if got, want := SingleLine(in), "saferedTAIL"; got != want {
		t.Fatalf("SingleLine() = %q, want %q", got, want)
	}
}

func TestMultilinePreservesReadableLayout(t *testing.T) {
	in := "café\tvalue\r\nsecond\x9b31mline\x9b0m"
	if got, want := Multiline(in), "café\tvalue\nsecondline"; got != want {
		t.Fatalf("Multiline() = %q, want %q", got, want)
	}
}

func TestControlStringsCannotHideFollowingText(t *testing.T) {
	in := "before\x1bPignored\x1b\\after"
	if got, want := SingleLine(in), "beforeafter"; got != want {
		t.Fatalf("SingleLine() = %q, want %q", got, want)
	}
}

func TestInvalidUTF8IsNotEmittedVerbatim(t *testing.T) {
	in := string([]byte{'a', 0xff, 'b'})
	if got, want := SingleLine(in), "a�b"; got != want {
		t.Fatalf("SingleLine() = %q, want %q", got, want)
	}
}
