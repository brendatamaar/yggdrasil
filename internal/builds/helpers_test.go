package builds_test

import (
	"io/fs"
	"os"
	"path/filepath"
)

func dirFS(dir string) fs.FS { return os.DirFS(dir) }

func mkfile(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
