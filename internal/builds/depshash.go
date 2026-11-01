package builds

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"io/fs"
	"sort"
)

// maxDepsFile is the largest dependency file HashDeps reads.
const maxDepsFile = 4 << 20

// DepsFiler is implemented by toolchains that know which files decide a
// worktree's dependencies (manifests and lock files). It is optional: callers
// use DepsFiles.
type DepsFiler interface {
	// DepsFiles returns the files, relative to the repo with forward slashes.
	// Files that don't exist are listed too, so adding one changes the hash.
	DepsFiles(d Detection) []string
}

// DepsFiles returns the dependency files of t for d, or nil when t does not
// implement DepsFiler. The result is a copy.
func DepsFiles(t Toolchain, d Detection) []string {
	f, ok := t.(DepsFiler)
	if !ok {
		return nil
	}
	return append([]string(nil), f.DepsFiles(d)...)
}

// HashDeps is the SHA-256 (hex) over the sorted, de-duplicated file names and
// their contents. A file that is missing, unreadable or over 4 MiB
// contributes a fixed marker instead of its content, never an error. The
// result does not depend on the order of files.
func HashDeps(repo fs.FS, files []string) string {
	names := append([]string(nil), files...)
	sort.Strings(names)
	h := sha256.New()
	var n [8]byte
	prev := ""
	for i, name := range names {
		if i > 0 && name == prev {
			continue
		}
		prev = name
		binary.BigEndian.PutUint64(n[:], uint64(len(name)))
		h.Write(n[:])
		_, _ = io.WriteString(h, name)
		data, ok := readDeps(repo, name)
		if !ok {
			h.Write([]byte{'M'}) // marker: missing, unreadable or too large
			continue
		}
		h.Write([]byte{'C'})
		binary.BigEndian.PutUint64(n[:], uint64(len(data)))
		h.Write(n[:])
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func readDeps(repo fs.FS, name string) ([]byte, bool) {
	f, err := repo.Open(name)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxDepsFile+1))
	if err != nil || len(data) > maxDepsFile {
		return nil, false
	}
	return data, true
}
