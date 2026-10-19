package repos_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/core"
	corerepos "github.com/brendatamaar/yggdrasil/internal/core/repos"
	"github.com/brendatamaar/yggdrasil/internal/db/dbtest"
	"github.com/brendatamaar/yggdrasil/internal/platform"
)

// Rows seeded by v0.2.0 keep the case the user typed. The core compares paths
// with ==, so the shell must canonicalise them when it loads the workspace.
func TestLegacyLowercasePathIsCanonicalised(t *testing.T) {
	if !platform.PathsCaseInsensitive {
		t.Skip("on-disk case only differs from the typed case on Windows")
	}
	e := newEnv(t)
	e.repoDir("shop-api")
	ctx := context.Background()
	legacy := strings.ToLower(e.root)
	p := dbtest.Project(t, e.store, core.Project{Name: "legacy", Path: legacy})
	if pv, err := e.svc.Preview(ctx, e.root); err != nil || pv.Existing == nil || *pv.Existing != p.ID {
		t.Fatalf("Preview of the canonical spelling = %+v, %v; want Existing %s", pv, err, p.ID)
	}
	out, err := e.svc.Track(ctx, e.root, "again", []corerepos.Pick{pick("shop-api")}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if tr, ok := out.(corerepos.Tracked); !ok || tr.NewProject || tr.ProjectID != p.ID {
		t.Errorf("same folder, canonical spelling = %#v, want Tracked in the existing project %s", out, p.ID)
	}
	if again, err := e.svc.Track(ctx, e.root, "again", []corerepos.Pick{pick("shop-api")}, "user"); err != nil {
		t.Fatal(err)
	} else if at, ok := again.(corerepos.AlreadyTracked); !ok || at.ProjectID != p.ID {
		t.Errorf("second track of the same repo = %#v, want AlreadyTracked", again)
	}
	sub := filepath.Join(e.root, "shop-api")
	if _, err := os.Stat(sub); err != nil {
		t.Fatal(err)
	}
	out, err = e.svc.Track(ctx, sub, "inner", []corerepos.Pick{pick(".")}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if ov, ok := out.(corerepos.Overlaps); !ok || ov.Other != p.ID {
		t.Errorf("folder inside the legacy project = %#v, want Overlaps(%s)", out, p.ID)
	}
}
