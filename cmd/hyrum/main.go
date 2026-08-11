// Command hyrum generates and runs Hyrum's tests: hermetic tests that capture
// how a target repository uses each of its dependencies.
//
// Subcommands:
//
//	surface   print the usage surface of one or all direct dependencies (no LLM)
//	gen       generate tests for one or all direct dependencies
//	check     run existing tests/hyrum against a candidate dependency version
//	corpus    build a producer-side corpus for one upstream across N dependents
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

const exitUsage = 2

var commands = map[string]func(context.Context, []string) error{
	"surface": cmdSurface,
	"gen":     cmdGen,
	"check":   cmdCheck,
	"corpus":  cmdCorpus,
}

func main() {
	os.Exit(run())
}

func run() int {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printUsage()
		if len(args) == 0 {
			return exitUsage
		}
		return 0
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "hyrum: unknown subcommand %q\n\n", args[0])
		printUsage()
		return exitUsage
	}
	// Cancel the context on SIGINT/SIGTERM so harness.Run's cmd.Cancel
	// terminates the backend's process group; without this a kill of hyrum
	// leaves the codex/claude subprocess running.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cmd(ctx, args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "hyrum %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

func printUsage() {
	fmt.Fprint(os.Stderr, `hyrum — generate and run Hyrum's tests

Usage:
  hyrum surface <path> [--dep name] [--json]
  hyrum gen     <path> [--dep name] [--out dir] [--backend claude]
  hyrum check   [--dep purl@version]
  hyrum corpus  <purl> [--dependents N]

`)
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return fmt.Sprint([]string(*s)) }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("hyrum "+name, flag.ExitOnError)
	return fs
}
