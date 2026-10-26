package guard

import (
	"fmt"
	"strings"
)

// Rule validation lives in the core (D11): ParsePolicy and Policy.Validate
// refuse the same rules as the settings parser, so a rule that can run
// anything never reaches the hook.

func nameSet(names ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[n] = struct{}{}
	}
	return m
}

var (
	// neverAllowed: shells and script hosts, git (every mutation is a
	// proposal), network clients and destructive or system tools.
	neverAllowed = nameSet(
		"sh", "bash", "zsh", "dash", "fish", "cmd", "powershell", "pwsh", "powershell_ise", "wscript", "cscript",
		"mshta", "wsl", "env", "xargs", "find", "start", "explorer",
		"git",
		"ssh", "scp", "curl", "wget", "nc", "ncat", "telnet", "ftp", "gh", "glab", "docker",
		"rm", "del", "erase", "rmdir", "format", "reg", "sc", "net", "runas", "mklink", "icacls", "takeown",
		"msiexec", "rundll32", "regsvr32", "certutil", "bitsadmin", "schtasks", "wmic", "robocopy", "xcopy",
		"move", "copy", "tar", "7z",
		// launchers of other programs, with or without elevation.
		"conhost", "wt", "forfiles", "sudo", "code")
	// interpreters run whatever script they get: only a fixed script is allowed.
	interpreters = nameSet("node", "npx", "tsx", "ts-node", "python", "python3", "pythonw", "py", "ruby", "perl",
		"php", "deno", "bun", "uvx", "pipx", "pnpx", "bunx", "corepack", "java", "jshell", "make")
	// wrappers live inside the repo, which the guard refuses to run from (D11).
	wrappers = nameSet("mvnw", "gradlew")
	// packageTools need a fixed subcommand, and not one that installs,
	// publishes or runs arbitrary code.
	scriptRunners  = nameSet("npm", "pnpm", "yarn")
	packageTools   = nameSet("npm", "pnpm", "yarn", "pip", "pip3", "uv", "cargo", "go", "dotnet", "mvn", "gradle")
	badSubcommands = nameSet("install", "add", "i", "publish", "exec", "dlx", "x", "remove", "uninstall", "update", "upgrade",
		"ci", "get", "run", "generate", "restore", "fetch", "tool", "rebuild", "link", "login", "config", "download", "sync")
)

// programBase is the lower-case name of a program without any extension
// (node.exe and Node.CMD are node).
func programBase(p string) string {
	p = strings.ToLower(p)
	if i := strings.IndexByte(p, '.'); i > 0 {
		p = p[:i]
	}
	return p
}

// plainWord is true when the command-line rules read s as exactly one word
// equal to s.
func plainWord(s string) bool {
	argv, err := ParseCommandLine(FormatCommandLine([]string{s}))
	return err == nil && len(argv) == 1 && argv[0] == s
}

func clip(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

func validateRule(r CommandRule) error {
	if !plainWord(r.Program) || strings.ContainsAny(r.Program, " :=%+") || strings.HasPrefix(r.Program, "-") ||
		r.Program == "." || r.Program == ".." {
		return fmt.Errorf("allowlist program %q must be a plain command name such as go or npm", clip(r.Program))
	}
	for _, a := range r.Args {
		if a == "" || !plainWord(a) {
			return fmt.Errorf("allowlist argument %q for %s holds a character that is not allowed", clip(a), r.Program)
		}
	}
	base := programBase(r.Program)
	if _, bad := neverAllowed[base]; bad {
		return fmt.Errorf("allowlist program %q can't be allowed: it can run anything or change files and remotes; use a proposal instead", clip(r.Program))
	}
	if _, w := wrappers[base]; w {
		return fmt.Errorf("allowlist program %q: build wrappers are not supported yet; allow the tool itself (for example mvn or gradle)", clip(r.Program))
	}
	_, isInterp := interpreters[base]
	_, isPkg := packageTools[base]
	if (isInterp || isPkg) && len(r.Args) > 0 && strings.HasPrefix(r.Args[0], "-") {
		return fmt.Errorf("allowlist rule %q can't start with the option %q: it changes what the rest of the command does; start with a plain script or subcommand", clip(r.Program), clip(r.Args[0]))
	}
	if isInterp && (len(r.Args) == 0 || r.Tail != TailNone) {
		return fmt.Errorf("allowlist program %q runs any script it is given; allow it only with fixed arguments and no extra arguments", clip(r.Program))
	}
	if isPkg {
		if len(r.Args) == 0 {
			return fmt.Errorf("allowlist program %q needs a fixed first argument such as test or build", clip(r.Program))
		}
		if sub := strings.ToLower(r.Args[0]); sub == "run" && isScriptRunner(base) {
			// npm/pnpm/yarn run <script>: the canonical test and build command.
			// It needs a named script; what the script does is the repo's own
			// (docs/design/guard.md decision 4, threat model).
			if len(r.Args) < 2 || strings.HasPrefix(r.Args[1], "-") {
				return fmt.Errorf("allowlist rule %q run needs a script name such as test or build", clip(r.Program))
			}
		} else if _, bad := badSubcommands[sub]; bad {
			return fmt.Errorf("allowlist rule %q %q can't be allowed: it installs, publishes or runs arbitrary code; use a proposal instead", clip(r.Program), clip(r.Args[0]))
		}
	}
	return nil
}

func isScriptRunner(base string) bool {
	_, ok := scriptRunners[base]
	return ok
}
