package guard

import "testing"

func TestAdmissibleIsNoneOrTrustedOnly(t *testing.T) {
	for _, tc := range []struct {
		v    TrustView
		want bool
	}{
		{TrustNone, true}, {TrustTrusted, true}, {TrustUntrusted, false}, {"", false}, {"bogus", false},
	} {
		if got := tc.v.Admissible(); got != tc.want {
			t.Errorf("%q: Admissible = %v, want %v", tc.v, got, tc.want)
		}
		if got := allReposTrusted([]TrustView{tc.v}); got != tc.want {
			t.Errorf("%q: allReposTrusted = %v, want %v (admission and verify must agree)", tc.v, got, tc.want)
		}
	}
}
