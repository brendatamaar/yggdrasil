// Command demo-gen writes the fictional acme-shop demo project (PRD IN-05):
// six git repos with a fixed history, local bare remotes and the special
// cases the status views need (ahead, diverged, behind, dirty, detached and
// conflicted repos, a CRLF file, tags), Maven, Gradle, Node, Go and uv
// manifests, and recorded GitLab, GitHub and Gitea answers under hosts/.
//
// It uses go-git and never starts a process. It never deletes files: the
// output folder must be named demo-project and must be empty or missing.
//
//	go run . -out <path>/demo-project
//	go run . -out <path>/demo-project -if-missing   # no-op when it exists
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	out := flag.String("out", "", "output folder, named "+outName+"; it must be empty or missing")
	ifMissing := flag.Bool("if-missing", false, "do nothing, successfully, when the output folder already has files")
	scenario := flag.String("scenario", "", "write a named scenario instead of the default project (known: "+scenarioMultiRepo+")")
	flag.Parse()
	if *out == "" || flag.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "usage: go run . -out <path>/"+outName+" [-if-missing] [-scenario "+scenarioMultiRepo+"]")
		os.Exit(2)
	}
	if *ifMissing {
		if entries, err := os.ReadDir(*out); err == nil && len(entries) > 0 {
			fmt.Println("demo-gen:", *out, "already exists: skipping (remove it yourself to regenerate)")
			return
		}
	}

	gen := generate
	if *scenario != "" {
		gen = func(o string) (string, []repoSummary, error) { return generateScenario(*scenario, o) }
	}
	root, sums, err := gen(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo-gen:", err)
		os.Exit(1)
	}
	printSummary(os.Stdout, root, sums)
}
