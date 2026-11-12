package proposals

import (
	"strings"
	"testing"
)

func TestCommentReceiptValidate(t *testing.T) {
	valid := CommentReceipt{CommentID: "C_shop-42", Marker: strings.Repeat("a", 16), BodyHash: strings.Repeat("b", 64), Head: strings.Repeat("c", 40)}
	for _, n := range []int{40, 64} {
		r := valid
		r.Head = strings.Repeat("d", n)
		if err := r.Validate(); err != nil {
			t.Fatalf("valid head length %d: %v", n, err)
		}
	}
	for _, n := range []int{16, 64} {
		r := valid
		r.Marker = strings.Repeat("e", n)
		if err := r.Validate(); err != nil {
			t.Fatalf("valid marker length %d: %v", n, err)
		}
	}
	r := valid
	r.CommentID = strings.Repeat("x", 200)
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"id":     {"", strings.Repeat("x", 201), "C shop", "C/shop", "C\\shop", "C?shop", "C:shop", "C@shop", "C#shop", "C\x00shop", "C\tshop", "C\rshop", "C\nshop", "C\x7fshop"},
		"marker": {"", strings.Repeat("a", 15), strings.Repeat("a", 65), strings.Repeat("A", 16), strings.Repeat("g", 16), "aaaaaaaaaaaaaaa\n"},
		"body":   {"", strings.Repeat("b", 7), strings.Repeat("b", 63), strings.Repeat("b", 65), strings.Repeat("B", 64), strings.Repeat("z", 64)},
		"head":   {"", strings.Repeat("c", 6), strings.Repeat("c", 7), strings.Repeat("c", 39), strings.Repeat("c", 41), strings.Repeat("c", 63), strings.Repeat("c", 65), strings.Repeat("C", 40), strings.Repeat("z", 40)},
	}
	for field, values := range cases {
		for _, bad := range values {
			t.Run(field+"/"+bad, func(t *testing.T) {
				r := valid
				switch field {
				case "id":
					r.CommentID = bad
				case "marker":
					r.Marker = bad
				case "body":
					r.BodyHash = bad
				case "head":
					r.Head = bad
				}
				if err := r.Validate(); err == nil {
					t.Errorf("accepted invalid %s: %q", field, bad)
				}
			})
		}
	}
}
