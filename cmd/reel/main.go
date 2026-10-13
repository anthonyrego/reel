// Command reel records the screen with system audio and a microphone mixed
// into one track. One binary, several subcommands; each lives in its own
// file and registers itself in init.
//
// Copied from an earlier project of the author's.
package main

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
)

type command struct {
	name    string
	summary string
	run     func(args []string) error
}

var commands = map[string]*command{}

func register(c *command) {
	if _, dup := commands[c.name]; dup {
		panic("duplicate subcommand " + c.name)
	}
	commands[c.name] = c
}

func main() {
	// No subcommand is the app (how the bundle starts). Finder may pass a
	// -psn_* argument; anything else starting with "-" is a flag for it.
	// (As an earlier project of the author's does.)
	name, args := "app", os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	args = slices.DeleteFunc(args, func(a string) bool { return strings.HasPrefix(a, "-psn_") })
	if name == "help" || name == "-h" || name == "--help" {
		usage()
		return
	}
	c := commands[name]
	if c == nil {
		fmt.Fprintf(os.Stderr, "reel: unknown subcommand %q\n\n", name)
		usage()
		os.Exit(2)
	}
	if err := c.run(args); err != nil {
		if err != errQuiet {
			fmt.Fprintf(os.Stderr, "reel %s: %v\n", name, err)
		}
		os.Exit(1)
	}
}

// errQuiet fails a command that has already said why.
var errQuiet = fmt.Errorf("failed")

func usage() {
	fmt.Fprintln(os.Stderr, "usage: reel <subcommand> [flags]\n\nsubcommands:")
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(os.Stderr, "  %-15s %s\n", n, commands[n].summary)
	}
}
