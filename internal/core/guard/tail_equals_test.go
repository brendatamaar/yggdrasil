package guard

import "testing"

// A .cmd or .bat wrapper (mvnw.cmd) splits an unquoted argument at "=", so
// `x=..` would reach it as two arguments. Tail words therefore never hold "=".
func TestTailArgRefusesEquals(t *testing.T) {
	roots := []string{"/work/repo"}
	for _, a := range []string{"x=..", "x=/etc", "a=b", "=x", "./a=../.."} {
		if tailArgOK(a, roots, false) {
			t.Errorf("tailArgOK(%q) = true, want false", a)
		}
	}
	for _, a := range []string{"./pkg", "pkg/sub", "name"} {
		if !tailArgOK(a, roots, false) {
			t.Errorf("tailArgOK(%q) = false, want true", a)
		}
	}
}
