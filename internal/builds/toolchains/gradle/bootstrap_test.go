package gradle

import (
	"reflect"
	"testing"
	"testing/fstest"
)

func TestBootstrap(t *testing.T) {
	d, _ := New().Detect(fstest.MapFS{"build.gradle": {Data: []byte("plugins { id 'java' }")}, "gradlew": {}})
	c := New().Commands(d)
	want := []string{"gradlew", "--console=plain", "dependencies"}
	if len(c.Bootstrap) != 1 || !reflect.DeepEqual(c.Bootstrap[0].Argv, want) || c.Bootstrap[0].Name == "" {
		t.Fatalf("bootstrap = %+v", c.Bootstrap)
	}
	for _, s := range c.Verify {
		if len(s.Env) != 0 {
			t.Errorf("verify env = %v", s.Env)
		}
	}
	d, _ = New().Detect(fstest.MapFS{"build.gradle": {}})
	if got := New().Commands(d).Bootstrap[0].Argv[0]; got != "gradle" {
		t.Fatalf("runner = %q", got)
	}
}

func TestVerifyAddsKotlinOnlyForKotlin(t *testing.T) {
	java, _ := New().Detect(fstest.MapFS{"build.gradle.kts": {Data: []byte(`plugins { java }`)}})
	if got := New().Commands(java).Verify[0].Argv; !reflect.DeepEqual(got, []string{"gradle", "compileJava", "compileTestJava"}) {
		t.Fatalf("java: %v", got)
	}
	kt, _ := New().Detect(fstest.MapFS{"build.gradle.kts": {Data: []byte(`plugins { kotlin("jvm") }`)}})
	want := []string{"gradle", "compileJava", "compileTestJava", "compileKotlin", "compileTestKotlin"}
	if got := New().Commands(kt).Verify[0].Argv; !reflect.DeepEqual(got, want) {
		t.Fatalf("kotlin: %v", got)
	}
}

func TestFullTests(t *testing.T) {
	d, _ := New().Detect(fstest.MapFS{"build.gradle": {}, "gradlew": {}})
	c := New().Commands(d)
	if len(c.FullTests) != 1 || !reflect.DeepEqual(c.FullTests[0].Argv, []string{"gradlew", "test"}) {
		t.Fatalf("%+v", c.FullTests)
	}
}

func TestDepsFiles(t *testing.T) {
	d, _ := New().Detect(fstest.MapFS{"build.gradle": {}})
	want := []string{"build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts",
		"gradle/wrapper/gradle-wrapper.properties", "gradle.lockfile"}
	if got := New().DepsFiles(d); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	d, _ = New().Detect(fstest.MapFS{"backend/build.gradle": {}})
	if got := New().DepsFiles(d); got[0] != "backend/build.gradle" || got[5] != "backend/gradle.lockfile" {
		t.Fatalf("%v", got)
	}
}
