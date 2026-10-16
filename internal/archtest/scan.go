package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"slices"
	"strconv"
	"strings"
)

// file is what the rules look at in one Go source file.
type file struct {
	// dir is the package directory relative to the module root, with slashes
	// ("internal/platform", "cmd/ygg").
	dir  string
	name string
	test bool
	// imports are the import paths.
	imports []string
	// uses are "importpath.Name" for every qualified identifier, such as
	// "os.Environ" or "runtime.GOOS".
	uses []string
	// methods are the names called as x.Name(...) on any value.
	methods []string
}

// parse reads one source file. src is the content; name is its file name.
func parse(dir, name string, src any) (file, error) {
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		return file{}, err
	}
	f := file{dir: dir, name: name, test: strings.HasSuffix(name, "_test.go")}
	local := map[string]string{} // import name -> import path
	for _, imp := range af.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		f.imports = append(f.imports, p)
		n := path.Base(p)
		if strings.HasPrefix(n, "v") && len(n) > 1 && n[1] >= '0' && n[1] <= '9' {
			n = path.Base(path.Dir(p)) // github.com/x/y/v3 is imported as y
		}
		if imp.Name != nil {
			n = imp.Name.Name
		}
		local[n] = p
	}
	ast.Inspect(af, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		f.methods = append(f.methods, sel.Sel.Name)
		if id, ok := sel.X.(*ast.Ident); ok {
			if p, ok := local[id.Name]; ok {
				f.uses = append(f.uses, p+"."+sel.Sel.Name)
			}
		}
		return true
	})
	return f, nil
}

func (f file) imports1(p string) bool { return slices.Contains(f.imports, p) }

func (f file) uses1(q string) bool { return slices.Contains(f.uses, q) }

func (f file) calls(method string) bool { return slices.Contains(f.methods, method) }
