package git

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestRemoteURL(t *testing.T) {
	f := newFx(t)
	dir := f.repo("linked")
	f.git(dir, "remote", "add", "origin", "https://git.acme.dev/acme-shop/shop-api.git")
	f.git(dir, "remote", "add", "upstream", "git@git.acme.dev:acme-shop/shop-api.git")

	got, err := f.g.RemoteURL(bg(), dir, "origin")
	if err != nil || got != "https://git.acme.dev/acme-shop/shop-api.git" {
		t.Fatalf("origin = %q, %v", got, err)
	}
	got, err = f.g.RemoteURL(bg(), dir, "upstream")
	if err != nil || got != "git@git.acme.dev:acme-shop/shop-api.git" {
		t.Fatalf("upstream = %q, %v", got, err)
	}
}

func TestRemoteURLWithSpacesInPath(t *testing.T) {
	f := newFx(t)
	dir := filepath.Join(t.TempDir(), "my repo")
	f.git(filepath.Dir(dir), "init", "-q", "-b", "main", dir)
	f.git(dir, "remote", "add", "origin", "https://github.com/acme/shop-web.git")
	got, err := f.g.RemoteURL(bg(), dir, "origin")
	if err != nil || got != "https://github.com/acme/shop-web.git" {
		t.Fatalf("RemoteURL = %q, %v", got, err)
	}
}

func TestRemoteURLNoRemote(t *testing.T) {
	f := newFx(t)
	dir := f.repo("lonely")
	_, err := f.g.RemoteURL(bg(), dir, "origin")
	if !errors.Is(err, ErrNoRemote) {
		t.Fatalf("repo without a remote = %v, want ErrNoRemote", err)
	}
	if msg := UserMessage(err); msg == "" || msg == err.Error() {
		t.Errorf("no plain-language message: %q", msg)
	}
}

func TestRemoteURLRefusesOptionLikeNames(t *testing.T) {
	f := newFx(t)
	dir := f.repo("opts")
	for _, name := range []string{"", "-v", "--all"} {
		if _, err := f.g.RemoteURL(bg(), dir, name); !errors.Is(err, ErrInvalidArg) {
			t.Errorf("RemoteURL(%q) = %v, want ErrInvalidArg", name, err)
		}
	}
}

func TestRemoteURLNotARepo(t *testing.T) {
	f := newFx(t)
	_, err := f.g.RemoteURL(bg(), t.TempDir(), "origin")
	if err == nil || errors.Is(err, ErrNoRemote) {
		t.Fatalf("plain folder = %v, want a git error that is not ErrNoRemote", err)
	}
}
