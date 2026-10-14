// Command demo-gen writes the fictional acme-shop demo project (PRD IN-05):
// three git repos with a fixed history, local bare remotes and a few special
// cases (ahead, diverged and behind branches, a dirty tree, a CRLF file, tags).
//
// It uses go-git and never starts a process. It never deletes files: the
// output folder must be named demo-project and must be empty or missing.
//
//	go run . -out <path>/demo-project
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	out := flag.String("out", "", "output folder, named "+outName+"; it must be empty or missing")
	flag.Parse()
	if *out == "" || flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: go run . -out <path>/"+outName)
		os.Exit(2)
	}

	root, sums, err := generate(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo-gen:", err)
		os.Exit(1)
	}
	printSummary(os.Stdout, root, sums)
}
