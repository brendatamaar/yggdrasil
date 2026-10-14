package main

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

type repoSummary struct {
	name     string
	commits  int
	branches []branchState
	tags     []string
	notes    []string
}

type branchState struct{ name, state string }

// summarize reads the finished repo back: commits reachable from local
// branches and origin/*, each branch's state against its upstream, and tags.
func summarize(name string, r *git.Repository) (repoSummary, error) {
	s := repoSummary{name: name}
	cfg, err := r.Config()
	if err != nil {
		return s, fmt.Errorf("read config: %w", err)
	}

	all := map[plumbing.Hash]bool{}
	refs, err := r.References()
	if err != nil {
		return s, fmt.Errorf("list refs: %w", err)
	}
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		n := ref.Name()
		if ref.Type() != plumbing.HashReference || !(n.IsBranch() || n.IsRemote()) {
			return nil
		}
		return ancestors(r, ref.Hash(), all)
	})
	if err != nil {
		return s, err
	}
	s.commits = len(all)

	branches, err := r.Branches()
	if err != nil {
		return s, fmt.Errorf("list branches: %w", err)
	}
	err = branches.ForEach(func(ref *plumbing.Reference) error {
		br := ref.Name().Short()
		if _, ok := cfg.Branches[br]; !ok {
			s.branches = append(s.branches, branchState{br, "local only"})
			return nil
		}
		up, err := refHash(r, plumbing.NewRemoteReferenceName("origin", br))
		if err != nil {
			return err
		}
		ahead, behind, err := aheadBehind(r, ref.Hash(), up)
		if err != nil {
			return err
		}
		s.branches = append(s.branches, branchState{br, describe("origin/"+br, ahead, behind)})
		return nil
	})
	if err != nil {
		return s, err
	}
	sort.Slice(s.branches, func(i, j int) bool {
		a, b := s.branches[i].name, s.branches[j].name
		if (a == "main") != (b == "main") {
			return a == "main"
		}
		return a < b
	})

	tags, err := r.Tags()
	if err != nil {
		return s, fmt.Errorf("list tags: %w", err)
	}
	err = tags.ForEach(func(ref *plumbing.Reference) error {
		t := ref.Name().Short()
		if _, err := r.TagObject(ref.Hash()); err == nil {
			t += " (annotated)"
		}
		s.tags = append(s.tags, t)
		return nil
	})
	sort.Strings(s.tags)
	return s, err
}

func describe(upstream string, ahead, behind int) string {
	switch {
	case ahead > 0 && behind > 0:
		return fmt.Sprintf("diverged from %s: %d ahead, %d behind", upstream, ahead, behind)
	case ahead > 0:
		return fmt.Sprintf("ahead of %s by %d", upstream, ahead)
	case behind > 0:
		return fmt.Sprintf("behind %s by %d", upstream, behind)
	}
	return "in sync with " + upstream
}

func aheadBehind(r *git.Repository, local, upstream plumbing.Hash) (int, int, error) {
	l, u := map[plumbing.Hash]bool{}, map[plumbing.Hash]bool{}
	if err := ancestors(r, local, l); err != nil {
		return 0, 0, err
	}
	if err := ancestors(r, upstream, u); err != nil {
		return 0, 0, err
	}
	ahead, behind := 0, 0
	for h := range l {
		if !u[h] {
			ahead++
		}
	}
	for h := range u {
		if !l[h] {
			behind++
		}
	}
	return ahead, behind, nil
}

// ancestors adds h and every commit it reaches to seen.
func ancestors(r *git.Repository, h plumbing.Hash, seen map[plumbing.Hash]bool) error {
	stack := []plumbing.Hash{h}
	for len(stack) > 0 {
		h := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[h] {
			continue
		}
		seen[h] = true
		c, err := r.CommitObject(h)
		if err != nil {
			return fmt.Errorf("read commit %s: %w", h, err)
		}
		stack = append(stack, c.ParentHashes...)
	}
	return nil
}

func printSummary(w io.Writer, root string, sums []repoSummary) {
	fmt.Fprintf(w, "acme-shop demo project written to %s\n", root)
	for _, s := range sums {
		fmt.Fprintf(w, "\n%s: %d commits\n", s.name, s.commits)
		for _, br := range s.branches {
			fmt.Fprintf(w, "  %-22s %s\n", br.name, br.state)
		}
		if len(s.tags) > 0 {
			fmt.Fprintf(w, "  tags: %s\n", strings.Join(s.tags, ", "))
		}
		for _, n := range s.notes {
			fmt.Fprintf(w, "  %s\n", n)
		}
	}
}
