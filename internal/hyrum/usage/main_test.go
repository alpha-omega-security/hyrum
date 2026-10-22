package usage

import (
	"os"
	"testing"

	"github.com/git-pkgs/outline"
)

func TestMain(m *testing.M) {
	// Race instrumentation can push small fixtures past the parse deadline.
	outline.SetParseTimeout(0)
	os.Exit(m.Run())
}
