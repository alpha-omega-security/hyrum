// Command hyrum generates and runs Hyrum's tests: hermetic tests that capture
// how a target repository uses each of its dependencies.
//
// Subcommands:
//
//	surface   print the usage surface of one or all direct dependencies (no LLM)
//	gen       generate tests for one or all direct dependencies
//	check     run existing tests/hyrum against a candidate dependency version
//	corpus    build a producer-side corpus for one upstream across N dependents
//
// The spike implements surface end to end and stubs the rest.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "surface":
		err = cmdSurface(os.Args[2:])
	case "gen":
		err = cmdGen(os.Args[2:])
	case "check":
		err = notYet("check")
	case "corpus":
		err = notYet("corpus")
	case "help", "-h", "--help":
		printUsage()
		return
	default:
		fmt.Fprintf(os.Stderr, "hyrum: unknown subcommand %q\n\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "hyrum %s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
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

func notYet(name string) error {
	return fmt.Errorf("%s: not implemented in the spike; see docs/design.md", name)
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return fmt.Sprint([]string(*s)) }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("hyrum "+name, flag.ExitOnError)
	return fs
}
