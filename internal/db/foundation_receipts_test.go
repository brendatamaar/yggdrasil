package db

import (
	"encoding/json"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
)

func foundationReceipts() []newrepos.Receipt {
	return []newrepos.Receipt{
		newrepos.DirReceipt{Identity: "volume:42:1"}, newrepos.GitReceipt{GitDirIdentity: "volume:42:2"},
		newrepos.FileReceipt{Name: "README.md", ContentHash: strings.Repeat("a", 64)},
		newrepos.FileReceipt{Name: ".gitignore", ContentHash: strings.Repeat("b", 64)},
		newrepos.CommitReceipt{Commit: strings.Repeat("c", 40), Tree: strings.Repeat("d", 64), MessageHash: strings.Repeat("e", 64), Author: "Alex <alex@acme.example>"},
		newrepos.TrackedReceipt{}, newrepos.RemoteReceipt{RemoteID: "R_shop", URL: "https://git.acme.example/acme/shop-api.git"},
		newrepos.OriginReceipt{URL: "https://git.acme.example/acme/shop-api.git"}, newrepos.PushReceipt{Head: strings.Repeat("f", 40)},
	}
}

// This oracle deliberately does not call any production validation or codec.
func receiptEvidenceValid(r newrepos.Receipt) bool {
	text := func(s string) bool { return len(s) > 0 && len(s) <= 4096 && !strings.ContainsAny(s, "\x00\r\n") }
	hash := func(s string) bool { return regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(s) }
	object := func(s string) bool { return regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`).MatchString(s) }
	remote := func(s string) bool {
		u, err := url.Parse(s)
		return err == nil && u.Scheme == "https" && u.Host != "" && u.Path != "" && u.Path != "/" && u.User == nil && !u.ForceQuery && u.RawQuery == "" && u.Fragment == "" && !strings.ContainsAny(s, "@?#%\\ \r\n\t\x00\x7f")
	}
	switch v := r.(type) {
	case newrepos.DirReceipt:
		return text(v.Identity)
	case newrepos.GitReceipt:
		return text(v.GitDirIdentity)
	case newrepos.FileReceipt:
		return (v.Name == "README.md" || v.Name == ".gitignore") && hash(v.ContentHash)
	case newrepos.CommitReceipt:
		return object(v.Commit) && object(v.Tree) && hash(v.MessageHash) && text(v.Author)
	case newrepos.TrackedReceipt:
		return true
	case newrepos.RemoteReceipt:
		return text(v.RemoteID) && remote(v.URL)
	case newrepos.OriginReceipt:
		return remote(v.URL)
	case newrepos.PushReceipt:
		return object(v.Head)
	}
	return false
}

func assertReceiptAcceptedValid(t *testing.T, raw string) {
	t.Helper()
	r, err := DecodeReceipt(raw)
	if err != nil {
		return
	}
	if !receiptEvidenceValid(r) {
		t.Fatalf("accepted invalid evidence: %s => %#v", raw, r)
	}
	var actual map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &actual); err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := json.Unmarshal(actual["kind"], &kind); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]string{
		"dir": " kind identity ", "git": " kind git_dir_identity ", "file": " kind name content_hash ",
		"commit": " kind commit tree message_hash author ", "tracked": " kind ", "remote": " kind remote_id url ",
		"origin": " kind url ", "push": " kind head ",
	}
	for key := range actual {
		if !strings.Contains(allowed[kind], " "+key+" ") {
			t.Fatalf("accepted foreign field %q: %s", key, raw)
		}
	}

}

func TestFoundationReceiptBoundary(t *testing.T) {
	fields := []string{"identity", "git_dir_identity", "name", "content_hash", "commit", "tree", "message_hash", "author", "remote_id", "url", "head"}
	for _, r := range foundationReceipts() {
		kind, raw, err := EncodeReceipt(r)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(kind+raw, func(t *testing.T) {
			back, err := DecodeReceipt(raw)
			if err != nil || !reflect.DeepEqual(back, r) {
				t.Fatalf("roundtrip %#v: %#v %v", r, back, err)
			}
			assertReceiptAcceptedValid(t, raw)
			var original map[string]json.RawMessage
			if err := json.Unmarshal([]byte(raw), &original); err != nil {
				t.Fatal(err)
			}
			for _, field := range fields {
				for _, value := range []string{`""`, `null`, `"wrong"`} {
					m := make(map[string]json.RawMessage)
					for k, v := range original {
						m[k] = v
					}
					m[field] = json.RawMessage(value)
					b, err := json.Marshal(m)
					if err != nil {
						t.Fatal(err)
					}
					if _, required := original[field]; required && value == `"wrong"` && (field == "identity" || field == "git_dir_identity" || field == "remote_id" || field == "author") {
						continue
					}
					if _, err := DecodeReceipt(string(b)); err == nil {
						t.Errorf("accepted broken %s=%s: %s", field, value, b)
					}
				}
				if _, ok := original[field]; ok {
					m := make(map[string]json.RawMessage)
					for k, v := range original {
						if k != field {
							m[k] = v
						}
					}
					b, err := json.Marshal(m)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := DecodeReceipt(string(b)); err == nil {
						t.Errorf("accepted missing %s", field)
					}
				}
			}
		})
	}
	// Break one domain field at a time, independently of the JSON decoder.
	for _, receipt := range foundationReceipts() {
		original := reflect.ValueOf(receipt)
		for field := 0; field < original.NumField(); field++ {
			for _, bad := range []string{"", "wrong", "bad\x00identity", strings.Repeat("A", 64), strings.Repeat("z", 4097)} {
				changed := reflect.New(original.Type()).Elem()
				changed.Set(original)
				changed.Field(field).SetString(bad)
				r, ok := changed.Interface().(newrepos.Receipt)
				if !ok {
					t.Fatal("fixture is not a receipt")
				}
				if receiptEvidenceValid(r) {
					continue
				}
				if _, _, err := EncodeReceipt(r); err == nil {
					t.Errorf("encoded invalid field: %#v", r)
				}
			}
		}
	}

	for _, raw := range []string{`{`, `null`, `{}`, `{"kind":"alien"}`, `{"kind":"tracked","extra":0}`, `{"kind":"tracked"} {}`} {
		if _, err := DecodeReceipt(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, bad := range []newrepos.Receipt{nil, newrepos.DirReceipt{}, newrepos.GitReceipt{}, newrepos.FileReceipt{}, newrepos.CommitReceipt{}, newrepos.RemoteReceipt{}, newrepos.OriginReceipt{}, newrepos.PushReceipt{}} {
		if _, _, err := EncodeReceipt(bad); err == nil {
			t.Errorf("encoded invalid %#v", bad)
		}
	}
	for _, badURL := range []string{"", "http://git.acme.example/a/b", "ssh://git.acme.example/a/b", "https://u:p@git.acme.example/a/b", "https://git.acme.example/a/b?token=x", "https://git.acme.example/a/b#x", "https://git.acme.example", "https://git.acme.example/a%20b", "https://git.acme.example:0/a/b", "https://git.acme.example:65536/a/b"} {
		for _, r := range []newrepos.Receipt{newrepos.RemoteReceipt{RemoteID: "R_shop", URL: badURL}, newrepos.OriginReceipt{URL: badURL}} {
			if _, _, err := EncodeReceipt(r); err == nil {
				t.Errorf("encoded unsafe URL %q", badURL)
			}
		}
		b, err := json.Marshal(map[string]string{"kind": "remote", "remote_id": "R_shop", "url": badURL})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeReceipt(string(b)); err == nil {
			t.Errorf("decoded unsafe URL %q", badURL)
		}
	}
}

func FuzzFoundationReceiptAcceptedImpliesValid(f *testing.F) {
	for _, r := range foundationReceipts() {
		_, raw, err := EncodeReceipt(r)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	for _, raw := range []string{`{"kind":"remote"}`, `{"kind":"dir","identity":"i","head":""}`, `null`, `{`} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) { assertReceiptAcceptedValid(t, raw) })
}
