package maven

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	corebuilds "github.com/brendatamaar/yggdrasil/internal/core/builds"
)

const corePOM = `<project>
  <parent><groupId>com.acme</groupId><version>1.2</version></parent>
  <artifactId>shop-core</artifactId>
  <dependencies>
    <dependency><groupId>com.acme</groupId><artifactId>shop-common</artifactId><version>1.2</version></dependency>
  </dependencies>
</project>`

func TestParsePOM(t *testing.T) {
	m, err := ParsePOM(strings.NewReader(corePOM))
	if err != nil {
		t.Fatal(err)
	}
	want := corebuilds.Module{
		Coord: corebuilds.Coord{Group: "com.acme", Artifact: "shop-core", Version: "1.2"},
		Deps:  []corebuilds.Coord{{Group: "com.acme", Artifact: "shop-common", Version: "1.2"}},
	}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("got %+v", m)
	}
}

func TestParsePOMRejects(t *testing.T) {
	cases := map[string]string{
		"malformed":      "<project><artifactId>x</project>",
		"not xml":        "hello",
		"no artifact":    "<project><groupId>g</groupId></project>",
		"blank artifact": "<project><artifactId>  </artifactId></project>",
		"too large":      "<project><artifactId>a</artifactId><!--" + strings.Repeat("x", MaxPOMSize) + "--></project>",
	}
	for name, in := range cases {
		if _, err := ParsePOM(strings.NewReader(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func FuzzParsePOM(f *testing.F) {
	f.Add(corePOM)
	f.Add("<project/>")
	f.Fuzz(func(t *testing.T, s string) {
		m, err := ParsePOM(bytes.NewReader([]byte(s)))
		if err == nil && m.Coord.Artifact == "" {
			t.Fatal("accepted a pom without artifactId")
		}
	})
}

func TestDetectWrapper(t *testing.T) {
	d, ok := New().Detect(fstest.MapFS{"pom.xml": {}, "mvnw.cmd": {}})
	if !ok || d.Runner != "mvnw" {
		t.Fatalf("%+v %v", d, ok)
	}
	d, _ = New().Detect(fstest.MapFS{"pom.xml": {}})
	if d.Runner != "mvn" {
		t.Fatalf("%+v", d)
	}
	if _, ok := New().Detect(fstest.MapFS{"build.gradle": {}}); ok {
		t.Fatal("matched gradle repo")
	}
}

func TestCommands(t *testing.T) {
	d, _ := New().Detect(fstest.MapFS{"pom.xml": {}, "mvnw": {}})
	c := New().Commands(d)
	if !reflect.DeepEqual(c.Verify[0].Argv, []string{"mvnw", "-B", "test-compile"}) ||
		!reflect.DeepEqual(c.FullTests[0].Argv, []string{"mvnw", "-B", "test"}) ||
		!reflect.DeepEqual(c.Build[0].Argv, []string{"mvnw", "clean", "install", "-B"}) {
		t.Fatalf("%+v", c)
	}
	if !reflect.DeepEqual(New().Artifacts(d), []string{"target/*.jar", "target/*.war"}) {
		t.Fatal(New().Artifacts(d))
	}
}

func TestModulesReactorAndOrder(t *testing.T) {
	repo := fstest.MapFS{
		"pom.xml": {Data: []byte(`<project><groupId>com.acme</groupId><artifactId>shop</artifactId><version>1</version>
<modules><module>api</module><module>../outside</module></modules></project>`)},
		"api/pom.xml": {Data: []byte(`<project><parent><groupId>com.acme</groupId><version>1</version></parent><artifactId>shop-api</artifactId>
<dependencies><dependency><groupId>com.acme</groupId><artifactId>shop-lib</artifactId></dependency></dependencies></project>`)},
	}
	d, _ := New().Detect(repo)
	mods, err := New().Modules(repo, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) != 2 || mods[1].Coord.Artifact != "shop-api" || mods[1].Coord.Group != "com.acme" {
		t.Fatalf("%+v", mods)
	}
}

func TestModulesCycleKeptTogether(t *testing.T) {
	mk := func(a, dep string) corebuilds.Module {
		return corebuilds.Module{Coord: corebuilds.Coord{Group: "g", Artifact: a},
			Deps: []corebuilds.Coord{{Group: "g", Artifact: dep}}}
	}
	a, b := mk("a", "b"), mk("b", "a")
	a.RepoID, b.RepoID = "ra", "rb"
	order, cycles := corebuilds.OrderModules([]corebuilds.Module{a, b})
	if len(order) != 2 || len(cycles) == 0 {
		t.Fatalf("order %v cycles %v", order, cycles)
	}
}

func TestModulesMissingModulePOM(t *testing.T) {
	repo := fstest.MapFS{"pom.xml": {Data: []byte(`<project><artifactId>s</artifactId><modules><module>gone</module></modules></project>`)}}
	d, _ := New().Detect(repo)
	if _, err := New().Modules(repo, d); err == nil {
		t.Fatal("expected error")
	}
}
