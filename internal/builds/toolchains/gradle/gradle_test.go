package gradle

import (
	"reflect"
	"testing"
	"testing/fstest"
)

func TestJava(t *testing.T) {
	d, ok := New().Detect(fstest.MapFS{"build.gradle": {Data: []byte("plugins { id 'java' }")}, "gradlew.bat": {}})
	if !ok || d.Runner != "gradlew" {
		t.Fatalf("%+v %v", d, ok)
	}
	c := New().Commands(d)
	if !reflect.DeepEqual(c.Verify[0].Argv, []string{"gradlew", "compileJava", "compileTestJava"}) {
		t.Fatalf("%v", c.Verify)
	}
	if c.FullTests[0].Argv[1] != "test" || New().Artifacts(d)[0] != "build/libs/*.jar" {
		t.Fatal("full tests or artifacts")
	}
}

func TestKotlin(t *testing.T) {
	d, ok := New().Detect(fstest.MapFS{"build.gradle.kts": {Data: []byte(`plugins { kotlin("jvm") }`)}})
	if !ok || d.Runner != "gradle" {
		t.Fatalf("%+v", d)
	}
	if got := New().Commands(d).Verify[0].Argv; !reflect.DeepEqual(got, []string{"gradle", "compileKotlin", "compileTestKotlin"}) {
		t.Fatal(got)
	}
}

func TestNoMatch(t *testing.T) {
	if _, ok := New().Detect(fstest.MapFS{"pom.xml": {}}); ok {
		t.Fatal("matched")
	}
}
