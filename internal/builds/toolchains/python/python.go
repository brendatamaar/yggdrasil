// Package python is the Python toolchain (BL-14). It handles uv, Poetry and
// pip with a venv; Pipenv, PDM, Hatch and conda use the custom command.
package python

import (
	"bufio"
	"bytes"
	"io/fs"
	"regexp"
	"strings"

	"github.com/brendatamaar/yggdrasil/internal/builds"
	"github.com/brendatamaar/yggdrasil/internal/builds/toolchains/internal/find"
)

const maxManifest = 4 << 20

// venvPython names the project's own interpreter, relative to the repo with
// forward slashes, like mvnw. internal/platform resolves it per OS.
const venvPython = ".venv/python"

// basePython is a reserved Argv[0] convention, like venvPython: the base
// interpreter that creates the venv, used before .venv exists. internal/platform
// resolves it with PythonBase from the clean environment.
const basePython = "python-base"

// Toolchain implements builds.Toolchain.
type Toolchain struct{}

// New returns the Python toolchain.
func New() Toolchain { return Toolchain{} }

// Name implements builds.Toolchain.
func (Toolchain) Name() string { return "python" }

// Detect looks for uv (uv.lock or [tool.uv]), then Poetry (poetry.lock or
// [tool.poetry]), then pip with a venv (requirements.txt). Runner is "uv",
// "poetry" or "pip". Facts hold "ruff", "mypy", "pyright" and "pytest" ("true"
// when configured), "django" when manage.py exists, "framework" (django,
// fastapi, flask or uvicorn) and "app" (the ASGI module for uvicorn). It
// reads files only.
func (Toolchain) Detect(repo fs.FS) (builds.Detection, bool) {
	dir, _, ok := find.Dir(repo, "pyproject.toml", "uv.lock", "poetry.lock", "requirements.txt")
	if !ok {
		return builds.Detection{}, false
	}
	has := func(n string) bool { return find.Exists(repo, find.Join(dir, n)) }
	pyproject := readCapped(repo, find.Join(dir, "pyproject.toml"))
	tables := tableNames(pyproject)
	hasTable := func(prefix string) bool {
		for _, t := range tables {
			if t == prefix || strings.HasPrefix(t, prefix+".") {
				return true
			}
		}
		return false
	}

	var runner string
	switch {
	case has("uv.lock") || hasTable("tool.uv"):
		runner = "uv"
	case has("poetry.lock") || hasTable("tool.poetry"):
		runner = "poetry"
	case has("requirements.txt"):
		runner = "pip"
	default:
		return builds.Detection{}, false
	}

	facts := map[string]string{}
	set := func(k string, on bool) {
		if on {
			facts[k] = "true"
		}
	}
	if has("uv.lock") {
		facts["locked"] = "true"
	}
	set("ruff", hasTable("tool.ruff") || has("ruff.toml") || has(".ruff.toml"))
	set("mypy", hasTable("tool.mypy") || has("mypy.ini") || has(".mypy.ini"))
	set("pyright", hasTable("tool.pyright") || has("pyrightconfig.json"))
	set("pytest", hasTable("tool.pytest") || has("pytest.ini") || has(".pytest.ini"))
	set("django", has("manage.py"))

	deps := strings.ToLower(pyproject + "\n" + readCapped(repo, find.Join(dir, "requirements.txt")))
	dep := func(name string) bool {
		return regexp.MustCompile(`(^|[^a-z0-9_.-])` + regexp.QuoteMeta(name) + `($|[^a-z0-9_.-])`).MatchString(deps)
	}
	switch {
	case has("manage.py") && dep("django"):
		facts["framework"] = "django"
	case dep("fastapi"):
		facts["framework"] = "fastapi"
		set("fastapicli", dep("fastapi-cli") || strings.Contains(deps, "fastapi[standard"))
	case dep("flask"):
		facts["framework"] = "flask"
	case dep("uvicorn"):
		facts["framework"] = "uvicorn"
	}
	for _, c := range []struct{ file, mod string }{
		{"main.py", "main:app"}, {"app.py", "app:app"}, {"app/main.py", "app.main:app"}, {"src/main.py", "src.main:app"},
	} {
		if has(c.file) {
			facts["app"] = c.mod
			break
		}
	}
	return builds.Detection{Toolchain: "python", Dir: dir, Runner: runner, Facts: facts}, true
}

func readCapped(repo fs.FS, name string) string {
	b, err := fs.ReadFile(repo, name)
	if err != nil || len(b) > maxManifest {
		return ""
	}
	return string(b)
}

// tableNames lists the lower-case table headers of a TOML text, such as
// "tool.ruff.lint". Array tables ([[x]]) are listed by their name too.
func tableNames(toml string) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader([]byte(toml)))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(l, "[") {
			continue
		}
		if i := strings.Index(l, "]"); i > 0 {
			name := strings.Trim(l[:i], "[] ")
			out = append(out, strings.ToLower(strings.ReplaceAll(name, " ", "")))
		}
	}
	return out
}

func on(d builds.Detection, k string) bool { return d.Facts[k] == "true" }

// env is the environment every step of the detection needs. Poetry steps all
// keep the venv in the project so bootstrap and later runs agree.
func env(d builds.Detection, extra ...string) []string {
	var out []string
	if d.Runner == "poetry" {
		out = append(out, "POETRY_VIRTUALENVS_IN_PROJECT=true")
	}
	return append(out, extra...)
}

func step(d builds.Detection, name string, argv []string, extra ...string) builds.Step {
	return builds.Step{Name: name, Argv: argv, Env: env(d, extra...)}
}

// tool runs a Python tool through the project's runner: "uv run ruff",
// "poetry run ruff", or ".venv/python -m ruff".
func tool(d builds.Detection, name string, args ...string) []string {
	switch d.Runner {
	case "uv", "poetry":
		return append([]string{d.Runner, "run", name}, args...)
	default:
		return append([]string{venvPython, "-m", name}, args...)
	}
}

// python runs the project's interpreter with args.
func python(d builds.Detection, args ...string) []string {
	switch d.Runner {
	case "uv", "poetry":
		return append([]string{d.Runner, "run", "python"}, args...)
	default:
		return append([]string{venvPython}, args...)
	}
}

func bootstrap(d builds.Detection) []builds.Step {
	switch d.Runner {
	case "uv":
		argv := []string{"uv", "sync"}
		if on(d, "locked") {
			argv = append(argv, "--frozen")
		}
		return []builds.Step{step(d, "Sync dependencies", argv)}
	case "poetry":
		return []builds.Step{step(d, "Install dependencies", []string{"poetry", "install"})}
	default:
		return []builds.Step{
			step(d, "Create virtual environment", []string{basePython, "-m", "venv", ".venv"}),
			step(d, "Install dependencies", []string{venvPython, "-m", "pip", "install", "-r", "requirements.txt"}),
		}
	}
}

// Commands implements builds.Toolchain. Verify is ruff, then mypy or
// pyright, then pytest, each only if configured, then manage.py check for
// Django; if nothing is configured it is compileall (docs/PRD.md 6.3).
func (Toolchain) Commands(d builds.Detection) builds.Commands {
	var c builds.Commands
	c.Install = bootstrap(d)
	c.Bootstrap = bootstrap(d)
	if on(d, "ruff") {
		c.Verify = append(c.Verify, step(d, "ruff", tool(d, "ruff", "check")))
	}
	switch {
	case on(d, "mypy"):
		c.Verify = append(c.Verify, step(d, "mypy", tool(d, "mypy", ".")))
	case on(d, "pyright"):
		c.Verify = append(c.Verify, step(d, "pyright", tool(d, "pyright")))
	}
	if on(d, "pytest") {
		c.Verify = append(c.Verify, step(d, "pytest", tool(d, "pytest")))
	} else {
		c.FullTests = []builds.Step{step(d, "pytest", tool(d, "pytest"))}
	}
	if on(d, "django") {
		c.Verify = append(c.Verify, step(d, "manage.py check", python(d, "manage.py", "check")))
	}
	if len(c.Verify) == 0 {
		c.Verify = []builds.Step{step(d, "compileall", python(d, "-m", "compileall", "-q", "."))}
	}
	// pip has no build command of its own; add one as a custom command.
	switch d.Runner {
	case "uv":
		c.Build = []builds.Step{step(d, "build", []string{"uv", "build"})}
	case "poetry":
		c.Build = []builds.Step{step(d, "build", []string{"poetry", "build"})}
	}
	c.DevServer = devServer(d)
	return c
}

// devServer picks the dev server from the framework found in the
// dependencies. Flask has a port variable; the others take the {port}
// placeholder in Argv (docs/phases/phase-5.md decision 2).
func devServer(d builds.Detection) []builds.Step {
	switch d.Facts["framework"] {
	case "django":
		return []builds.Step{step(d, "runserver", python(d, "manage.py", "runserver", "127.0.0.1:{port}"))}
	case "flask":
		return []builds.Step{step(d, "flask run", tool(d, "flask", "run"), "FLASK_RUN_PORT={port}")}
	case "fastapi":
		if on(d, "fastapicli") {
			return []builds.Step{step(d, "fastapi dev", tool(d, "fastapi", "dev", "--port", "{port}"))}
		}
		fallthrough
	case "uvicorn":
		app := d.Facts["app"]
		if app == "" {
			app = "main:app"
		}
		return []builds.Step{step(d, "uvicorn", tool(d, "uvicorn", app, "--port", "{port}"))}
	}
	return nil
}

// Artifacts implements builds.Toolchain: wheels and source archives.
func (Toolchain) Artifacts(d builds.Detection) []string {
	return []string{find.Join(d.Dir, "dist/*.whl"), find.Join(d.Dir, "dist/*.tar.gz")}
}

// DepsFiles implements builds.DepsFiler.
func (Toolchain) DepsFiles(d builds.Detection) []string {
	var lock string
	switch d.Runner {
	case "uv":
		lock = "uv.lock"
	case "poetry":
		lock = "poetry.lock"
	default:
		lock = "requirements.txt"
	}
	return []string{find.Join(d.Dir, "pyproject.toml"), find.Join(d.Dir, lock)}
}
