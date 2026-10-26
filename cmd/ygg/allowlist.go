package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains"
	"github.com/brendatamaar/yggdrasil/internal/core"
	"github.com/brendatamaar/yggdrasil/internal/core/guard"
	"github.com/brendatamaar/yggdrasil/internal/db"
	"github.com/brendatamaar/yggdrasil/internal/safety"
)

// allowlistKey is the setting that overrides a repo's command allowlist.
func allowlistKey(repo core.RepoID) string { return "allowlist:" + string(repo) }

// newAllowlist returns tasks.Config.Allowlist (SF-05): the repo's override from
// the settings when there is one, else the defaults derived from the repo's
// toolchains. A stored override that can't be read is an error, so no command
// runs rather than a wrong set.
func newAllowlist(store *db.Store, log *slog.Logger) func(context.Context, core.Repo) ([]guard.CommandRule, error) {
	return func(ctx context.Context, repo core.Repo) ([]guard.CommandRule, error) {
		s, err := store.GetSetting(ctx, allowlistKey(repo.ID))
		switch {
		case err == nil:
			rules, perr := safety.ParseAllowlist([]byte(s.Value))
			if perr != nil {
				return nil, fmt.Errorf("the command allowlist of repo %s is not valid: %w", repo.ID, perr)
			}
			return rules, nil
		case !errors.Is(err, db.ErrNotFound):
			return nil, fmt.Errorf("read the command allowlist of repo %s: %w", repo.ID, err)
		}
		return defaultRules(os.DirFS(repo.Path), log), nil
	}
}

// defaultRules derives rules from the verify, test and build steps of every
// toolchain that detects the repo, plus the Go commands for a go.mod. A rule
// the guard refuses is skipped and logged. Install and dev server steps are
// never allowed: installing is a proposal (invariant 2).
func defaultRules(repo fs.FS, log *slog.Logger) []guard.CommandRule {
	var steps [][]string
	for _, m := range toolchains.DetectAll(repo) {
		c := m.Toolchain.Commands(m.Detection)
		for _, list := range [][]builds.Step{c.Verify, c.FullTests, c.Build} {
			for _, st := range list {
				steps = append(steps, st.Argv)
			}
		}
	}
	if _, err := fs.Stat(repo, "go.mod"); err == nil {
		steps = append(steps, []string{"go", "vet", "./..."}, []string{"go", "build", "./..."}, []string{"go", "test", "./..."})
	}
	var rules []guard.CommandRule
	seen := map[string]bool{}
	for _, argv := range steps {
		r, ok := ruleOf(argv)
		if !ok {
			continue
		}
		if err := r.Validate(); err != nil {
			if log != nil {
				log.Info("a default command is not allowed for agents and was left out", "command", strings.Join(argv, " "), "reason", err.Error())
			}
			continue
		}
		key := r.Program + "\x00" + strings.Join(r.Args, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		rules = append(rules, r)
	}
	return rules
}

// ruleOf turns a step's argv into a rule with fixed arguments and no tail. The
// guard refuses build wrappers, so mvnw and gradlew become mvn and gradle.
// Options before the first plain argument (mvn -B test) are left out: the guard
// refuses a rule that starts with one.
func ruleOf(argv []string) (guard.CommandRule, bool) {
	if len(argv) == 0 {
		return guard.CommandRule{}, false
	}
	prog := strings.ToLower(argv[0])
	if i := strings.IndexByte(prog, '.'); i > 0 {
		prog = prog[:i]
	}
	switch prog {
	case "mvnw":
		prog = "mvn"
	case "gradlew":
		prog = "gradle"
	}
	args := argv[1:]
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		args = args[1:]
	}
	return guard.CommandRule{Program: prog, Args: append([]string(nil), args...), Tail: guard.TailNone}, true
}
