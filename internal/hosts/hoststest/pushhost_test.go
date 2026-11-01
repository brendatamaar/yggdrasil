package hoststest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/hosts/hoststest"
)

const apiBase = "https://git.acme-shop.example/acme-shop/shop-api"

func pushBranch(t *testing.T, h *hoststest.PushHost, work, branch string, opts ...string) string {
	t.Helper()
	h.MustGit(work, "switch", "-q", "-c", branch)
	h.WriteFile(work, branch+".txt", branch+"\n")
	h.MustGit(work, "add", ".")
	h.MustGit(work, "commit", "-q", "-m", "work on "+branch)
	args := []string{"push", "origin", branch}
	for _, o := range opts {
		args = append(args, "-o", o)
	}
	out, err := h.Git(work, args...)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	return out
}

func TestPushHostRecordsOptionsInOrder(t *testing.T) {
	h := hoststest.NewPushHost(t, apiBase)
	work := h.NewWork("shop-api")
	if !strings.Contains(h.Dir, " ") || !strings.Contains(work, " ") {
		t.Fatalf("paths should contain a space: %q %q", h.Dir, work)
	}
	pushBranch(t, h, work, "ygg/swift-fern", "merge_request.create", "merge_request.target=main", "merge_request.description=Links: "+hoststest.LinkMarker)
	pushes := h.Pushes()
	// NewWork's own push of main carries no options; the second push has three.
	if len(pushes) != 2 {
		t.Fatalf("pushes = %+v, want 2", pushes)
	}
	if len(pushes[0].Options) != 0 {
		t.Errorf("first push options = %v, want none", pushes[0].Options)
	}
	got := pushes[1]
	want := []string{"merge_request.create", "merge_request.target=main", "merge_request.description=Links: " + hoststest.LinkMarker}
	if strings.Join(got.Options, "|") != strings.Join(want, "|") {
		t.Errorf("options = %q, want %q", got.Options, want)
	}
	if len(got.Refs) != 1 || got.Refs[0] != "refs/heads/ygg/swift-fern" {
		t.Errorf("refs = %v", got.Refs)
	}
	if v, ok := got.Option("merge_request.target"); !ok || v != "main" {
		t.Errorf("Option(target) = %q, %v", v, ok)
	}
	if v, ok := got.Option("merge_request.create"); !ok || v != "" {
		t.Errorf("Option(create) = %q, %v", v, ok)
	}
	if _, ok := got.Option("nope"); ok {
		t.Error("Option(nope) found")
	}
}

func TestPushHostPrintsMergeRequestURL(t *testing.T) {
	h := hoststest.NewPushHost(t, apiBase)
	work := h.NewWork("shop-api")
	out := pushBranch(t, h, work, "ygg/one", "merge_request.create")
	if !strings.Contains(out, "remote: View merge request for ygg/one:") || !strings.Contains(out, "remote:   "+apiBase+"/-/merge_requests/1") {
		t.Fatalf("push output lacks the MR URL line:\n%s", out)
	}
	out = pushBranch(t, h, work, "ygg/two", "merge_request.create")
	if !strings.Contains(out, apiBase+"/-/merge_requests/2") {
		t.Fatalf("second MR should be number 2:\n%s", out)
	}
}

func TestPushHostNoURLWithoutCreateOption(t *testing.T) {
	h := hoststest.NewPushHost(t, apiBase)
	work := h.NewWork("shop-api")
	out := pushBranch(t, h, work, "ygg/plain", "ci.skip")
	if strings.Contains(out, "View merge request") {
		t.Fatalf("no create option, but the host printed an MR line:\n%s", out)
	}
	if v, ok := h.Pushes()[1].Option("ci.skip"); !ok || v != "" {
		t.Errorf("ci.skip not recorded: %+v", h.Pushes())
	}
}

func TestPushHostHookIsPortableAndLF(t *testing.T) {
	h := hoststest.NewPushHost(t, apiBase)
	b, err := os.ReadFile(filepath.Join(h.Dir, "hooks", "post-receive"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, "\r") {
		t.Error("hook has CR characters: sh would fail with CRLF")
	}
	if !strings.HasPrefix(s, "#!/bin/sh\n") {
		t.Error("hook must start with #!/bin/sh")
	}
	for _, bashism := range []string{"[[", "<<<", "${!", "local ", "function ", "pipefail"} {
		if strings.Contains(s, bashism) {
			t.Errorf("hook uses %q, which is not portable sh", bashism)
		}
	}
}

func TestPushHostAdvertisesPushOptions(t *testing.T) {
	h := hoststest.NewPushHost(t, apiBase)
	out := strings.TrimSpace(h.MustGit(h.Dir, "config", "--get", "receive.advertisePushOptions"))
	if out != "true" {
		t.Fatalf("receive.advertisePushOptions = %q", out)
	}
}

func TestPushHostTagPushPrintsNoMRLine(t *testing.T) {
	h := hoststest.NewPushHost(t, apiBase)
	work := h.NewWork("shop-api")
	h.MustGit(work, "tag", "v0.0.1")
	out, err := h.Git(work, "push", "origin", "v0.0.1", "-o", "merge_request.create")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "View merge request") {
		t.Fatalf("tag push printed an MR line:\n%s", out)
	}
}
