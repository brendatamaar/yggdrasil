package devservers_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brendatamaar/yggdrasil/internal/devservers"
)

func TestFileSinkRotatesAndKeepsThreeFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	s, err := devservers.NewFileSink(dir, "dsv_a", nil, 10, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		s.Write(strings.Repeat("x", 30))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		info, _ := e.Info()
		if info.Size() > 100 {
			t.Errorf("%s is %d bytes, over the limit", e.Name(), info.Size())
		}
	}
	want := []string{"dsv_a.log", "dsv_a.log.1", "dsv_a.log.2"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("files = %v, want %v", names, want)
	}
}

func TestFileSinkWithAnUnwritableFolderFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := devservers.NewFileSink(filepath.Join(file, "sub"), "dsv_a", nil, 10, 0, nil); err == nil {
		t.Error("a log folder under a file was created")
	}
}
