package maven

import (
	"reflect"
	"testing"
	"testing/fstest"
)

func TestBootstrap(t *testing.T) {
	d, _ := New().Detect(fstest.MapFS{"pom.xml": {}, "mvnw": {}})
	c := New().Commands(d)
	want := []string{"mvnw", "-B", "-ntp", "dependency:go-offline"}
	if len(c.Bootstrap) != 1 || !reflect.DeepEqual(c.Bootstrap[0].Argv, want) || c.Bootstrap[0].Name == "" {
		t.Fatalf("bootstrap = %+v", c.Bootstrap)
	}
	d, _ = New().Detect(fstest.MapFS{"pom.xml": {}})
	if got := New().Commands(d).Bootstrap[0].Argv[0]; got != "mvn" {
		t.Fatalf("runner = %q", got)
	}
}

func TestVerifyAndFullTests(t *testing.T) {
	d, _ := New().Detect(fstest.MapFS{"pom.xml": {}, "mvnw": {}})
	c := New().Commands(d)
	if !reflect.DeepEqual(c.Verify[0].Argv, []string{"mvnw", "-B", "test-compile"}) ||
		!reflect.DeepEqual(c.FullTests[0].Argv, []string{"mvnw", "-B", "test"}) {
		t.Fatalf("%+v %+v", c.Verify, c.FullTests)
	}
}

const rootPOM = `<project><groupId>com.acme</groupId><artifactId>shop</artifactId><version>1</version>
<packaging>pom</packaging><modules><module>shop-api</module><module>shop-web</module></modules></project>`

func TestDepsFilesSingleAndReactor(t *testing.T) {
	d, _ := New().Detect(fstest.MapFS{"pom.xml": {}})
	if got := New().DepsFiles(d); !reflect.DeepEqual(got, []string{"pom.xml"}) {
		t.Fatalf("%v", got)
	}
	m := fstest.MapFS{
		"pom.xml":          {Data: []byte(rootPOM)},
		"shop-api/pom.xml": {Data: []byte(`<project><parent><groupId>com.acme</groupId><version>1</version></parent><artifactId>shop-api</artifactId></project>`)},
		"shop-web/pom.xml": {Data: []byte(`<project><parent><groupId>com.acme</groupId><version>1</version></parent><artifactId>shop-web</artifactId></project>`)},
	}
	d, ok := New().Detect(m)
	if !ok {
		t.Fatal("no match")
	}
	want := []string{"pom.xml", "shop-api/pom.xml", "shop-web/pom.xml"}
	if got := New().DepsFiles(d); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v, want %v", got, want)
	}
}

func TestDepsFilesIgnoresEscapingModules(t *testing.T) {
	m := fstest.MapFS{"pom.xml": {Data: []byte(`<project><groupId>g</groupId><artifactId>a</artifactId><version>1</version>
<modules><module>../outside</module></modules></project>`)}}
	d, _ := New().Detect(m)
	if got := New().DepsFiles(d); !reflect.DeepEqual(got, []string{"pom.xml"}) {
		t.Fatalf("%v", got)
	}
}
