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

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) < 2 {
		printUsage()
		return 2
	}
	// Cancel the context on SIGINT/SIGTERM so harness.Run's cmd.Cancel
	// terminates the backend's process group; without this a kill of hyrum
	// leaves the codex/claude subprocess running.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "surface":
		err = cmdSurface(ctx, os.Args[2:])
	case "gen":
		err = cmdGen(ctx, os.Args[2:])
	case "check":
		err = cmdCheck(ctx, os.Args[2:])
	case "corpus":
		err = cmdCorpus(ctx, os.Args[2:])
	case "help", "-h", "--help":
		printUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "hyrum: unknown subcommand %q\n\n", os.Args[1])
		printUsage()
		return 2
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "hyrum %s: %v\n", os.Args[1], err)
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
