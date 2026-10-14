// Command demo opens (or creates) a SQLite database with the spike's setup,
// adds the acme-shop project with two repos and lists them.
//
//	CGO_ENABLED=0 go build -o bin/demo.exe ./cmd/demo
//	bin/demo.exe "C:\some dir\ygg.db"
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	db "github.com/brendatamaar/yggdrasil/spikes/sqlite"
	"github.com/brendatamaar/yggdrasil/spikes/sqlite/gen"
)

func main() {
	path := filepath.Join(os.TempDir(), "ygg sqlite spike", "ygg.db")
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	if err := run(context.Background(), path); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	d, err := db.Open(ctx, path)
	if err != nil {
		return err
	}
	defer d.Close()

	err = d.WithTx(ctx, func(q *gen.Queries) error {
		p, err := q.GetProjectByName(ctx, "acme-shop")
		if !errors.Is(err, sql.ErrNoRows) {
			return err // nil when the project already exists
		}
		if p, err = q.CreateProject(ctx, "acme-shop"); err != nil {
			return err
		}
		for _, name := range []string{"shop-api", "shop-web"} {
			if _, err := q.CreateRepo(ctx, gen.CreateRepoParams{ProjectID: p.ID, Name: name, Path: name}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	projects, err := d.Reader().ListProjects(ctx)
	if err != nil {
		return err
	}
	fmt.Println("database:", path)
	for _, p := range projects {
		repos, err := d.Reader().ListReposByProject(ctx, p.ID)
		if err != nil {
			return err
		}
		for _, r := range repos {
			fmt.Printf("%s/%s\n", p.Name, r.Name)
		}
	}
	return nil
}
