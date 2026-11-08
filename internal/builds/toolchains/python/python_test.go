package python

import (
	"io/fs"
	"os"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/brendatamaar/yggdrasil/internal/builds"
)

func argvs(ss []builds.Step) [][]string {
	var out [][]string
	for _, s := range ss {
		out = append(out, s.Argv)
	}
	return out
}

func detect(t *testing.T, repo fs.FS) builds.Detection {
	t.Helper()
	d, ok := New().Detect(repo)
	if !ok {
		t.Fatal("not detected")
	}
	return d
}

func TestUvFastAPI(t *testing.T) {
	d := detect(t, os.DirFS("testdata/uv-fastapi"))
	if d.Runner != "uv" || d.Facts["framework"] != "fastapi" || d.Facts["fastapicli"] != "true" {
		t.Fatalf("%+v", d)
	}
	c := New().Commands(d)
	if got := argvs(c.Bootstrap); !reflect.DeepEqual(got, [][]string{{"uv", "sync", "--frozen"}}) {
		t.Fatal(got)
	}
	if got := argvs(c.Verify); !reflect.DeepEqual(got, [][]string{{"uv", "run", "ruff", "check"}, {"uv", "run", "pytest"}}) {
		t.Fatal(got)
	}
	if len(c.FullTests) != 0 {
		t.Fatal("pytest is already in verify")
	}
	s := c.DevServer[0]
	if !reflect.DeepEqual(s.Argv, []string{"uv", "run", "fastapi", "dev", "--port", "{port}"}) || len(s.Env) != 0 {
		t.Fatalf("%+v", s)
	}
	if !reflect.DeepEqual(argvs(c.Build), [][]string{{"uv", "build"}}) {
		t.Fatal(c.Build)
	}
}

func TestPoetryFlask(t *testing.T) {
	d := detect(t, os.DirFS("testdata/poetry-flask"))
	if d.Runner != "poetry" || d.Facts["framework"] != "flask" {
		t.Fatalf("%+v", d)
	}
	c := New().Commands(d)
	venv := []string{"POETRY_VIRTUALENVS_IN_PROJECT=true"}
	if b := c.Bootstrap[0]; !reflect.DeepEqual(b.Argv, []string{"poetry", "install"}) || !reflect.DeepEqual(b.Env, venv) {
		t.Fatalf("%+v", b)
	}
	if got := argvs(c.Verify); !reflect.DeepEqual(got, [][]string{{"poetry", "run", "mypy", "."}}) {
		t.Fatal(got)
	}
	if got := argvs(c.FullTests); !reflect.DeepEqual(got, [][]string{{"poetry", "run", "pytest"}}) {
		t.Fatal(got)
	}
	s := c.DevServer[0]
	if !reflect.DeepEqual(s.Argv, []string{"poetry", "run", "flask", "run"}) ||
		!reflect.DeepEqual(s.Env, append(venv, "FLASK_RUN_PORT={port}")) {
		t.Fatalf("%+v", s)
	}
}

func TestPipDjango(t *testing.T) {
	d := detect(t, os.DirFS("testdata/pip-django"))
	if d.Runner != "pip" || d.Facts["django"] != "true" || d.Facts["framework"] != "django" {
		t.Fatalf("%+v", d)
	}
	c := New().Commands(d)
	if got := argvs(c.Bootstrap); !reflect.DeepEqual(got, [][]string{
		{"python", "-m", "venv", ".venv"},
		{".venv/python", "-m", "pip", "install", "-r", "requirements.txt"},
	}) {
		t.Fatal(got)
	}
	if got := argvs(c.Verify); !reflect.DeepEqual(got, [][]string{{".venv/python", "manage.py", "check"}}) {
		t.Fatal(got)
	}
	if got := argvs(c.DevServer); !reflect.DeepEqual(got, [][]string{{".venv/python", "manage.py", "runserver", "127.0.0.1:{port}"}}) {
		t.Fatal(got)
	}
	if len(c.Build) != 0 {
		t.Fatal("pip has no default build")
	}
}

type files = fstest.MapFS

func TestPlainPipFallsBackToCompileall(t *testing.T) {
	d := detect(t, files{"requirements.txt": {Data: []byte("requests\n")}})
	c := New().Commands(d)
	if got := argvs(c.Verify); !reflect.DeepEqual(got, [][]string{{".venv/python", "-m", "compileall", "-q", "."}}) {
		t.Fatal(got)
	}
	if len(c.DevServer) != 0 {
		t.Fatal("no framework, no dev server")
	}
	if got := argvs(c.FullTests); !reflect.DeepEqual(got, [][]string{{".venv/python", "-m", "pytest"}}) {
		t.Fatal(got)
	}
}

func TestDetectionRules(t *testing.T) {
	cases := []struct {
		name   string
		repo   files
		runner string // "" means not detected
	}{
		{"uv.lock", files{"uv.lock": {}}, "uv"},
		{"tool.uv", files{"pyproject.toml": {Data: []byte("[tool.uv]\n")}}, "uv"},
		{"poetry.lock", files{"poetry.lock": {}}, "poetry"},
		{"tool.poetry", files{"pyproject.toml": {Data: []byte("[tool.poetry]\n")}}, "poetry"},
		{"requirements", files{"requirements.txt": {}}, "pip"},
		{"uv beats requirements", files{"uv.lock": {}, "requirements.txt": {}}, "uv"},
		{"poetry beats requirements", files{"poetry.lock": {}, "requirements.txt": {}}, "poetry"},
		{"pdm only", files{"pyproject.toml": {Data: []byte("[tool.pdm]\n")}, "pdm.lock": {}}, ""},
		{"pipenv only", files{"Pipfile": {}}, ""},
		{"empty pyproject", files{"pyproject.toml": {}}, ""},
		{"sub folder", files{"api/uv.lock": {}}, "uv"},
		{"not python", files{"go.mod": {}}, ""},
	}
	for _, tc := range cases {
		d, ok := New().Detect(tc.repo)
		if ok != (tc.runner != "") || d.Runner != tc.runner {
			t.Errorf("%s: %+v %v", tc.name, d, ok)
		}
	}
}

func TestToolConfigurationRules(t *testing.T) {
	cases := []struct {
		name  string
		files files
		want  [][]string
	}{
		{"ruff.toml", files{"uv.lock": {}, "ruff.toml": {}}, [][]string{{"uv", "run", "ruff", "check"}}},
		{"pyright", files{"uv.lock": {}, "pyrightconfig.json": {}}, [][]string{{"uv", "run", "pyright"}}},
		{"mypy beats pyright", files{"uv.lock": {}, "mypy.ini": {}, "pyrightconfig.json": {}}, [][]string{{"uv", "run", "mypy", "."}}},
		{"pytest.ini", files{"uv.lock": {}, "pytest.ini": {}}, [][]string{{"uv", "run", "pytest"}}},
		{"table prefix only", files{"uv.lock": {}, "pyproject.toml": {Data: []byte("[tool.ruffle]\n")}}, [][]string{{"uv", "run", "python", "-m", "compileall", "-q", "."}}},
		{"nested table", files{"uv.lock": {}, "pyproject.toml": {Data: []byte("[tool.ruff.lint]\nselect=[]\n")}}, [][]string{{"uv", "run", "ruff", "check"}}},
	}
	for _, tc := range cases {
		d := detect(t, tc.files)
		if got := argvs(New().Commands(d).Verify); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}

func TestUnlockedUvSyncAndUvicorn(t *testing.T) {
	d := detect(t, files{
		"pyproject.toml": {Data: []byte("[tool.uv]\n[project]\ndependencies = [\"uvicorn\"]\n")},
		"app/main.py":    {},
	})
	c := New().Commands(d)
	if got := argvs(c.Bootstrap); !reflect.DeepEqual(got, [][]string{{"uv", "sync"}}) {
		t.Fatal(got)
	}
	if got := argvs(c.DevServer); !reflect.DeepEqual(got, [][]string{{"uv", "run", "uvicorn", "app.main:app", "--port", "{port}"}}) {
		t.Fatal(got)
	}
}

func TestFastAPIWithoutCLIUsesUvicornAndLookalikesAreIgnored(t *testing.T) {
	d := detect(t, files{"requirements.txt": {Data: []byte("fastapi==0.110\n")}, "main.py": {}})
	if got := argvs(New().Commands(d).DevServer); !reflect.DeepEqual(got, [][]string{{".venv/python", "-m", "uvicorn", "main:app", "--port", "{port}"}}) {
		t.Fatal(got)
	}
	d = detect(t, files{"requirements.txt": {Data: []byte("flask-cors==4\nfastapi-utils\n")}})
	if d.Facts["framework"] != "" {
		t.Fatalf("%+v", d)
	}
}

func TestDepsFilesAndArtifacts(t *testing.T) {
	d := detect(t, files{"api/poetry.lock": {}})
	if got := New().DepsFiles(d); !reflect.DeepEqual(got, []string{"api/pyproject.toml", "api/poetry.lock"}) {
		t.Fatal(got)
	}
	if got := New().Artifacts(d); !reflect.DeepEqual(got, []string{"api/dist/*.whl", "api/dist/*.tar.gz"}) {
		t.Fatal(got)
	}
}
