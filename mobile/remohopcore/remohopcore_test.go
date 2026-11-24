package remohopcore

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// exported returns "Name(params) results" for every exported top-level
// function in the non-test files of dir.
func exported(t *testing.T, dir string) map[string]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	out := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || !fd.Name.IsExported() {
				continue
			}
			var b strings.Builder
			printer.Fprint(&b, fset, fd.Type)
			out[fd.Name.Name] = b.String()
		}
	}
	return out
}

// TestWrapperMatchesCore keeps the bound package in step with
// internal/mobile: same functions, same signatures.
func TestWrapperMatchesCore(t *testing.T) {
	core := exported(t, filepath.Join("..", "..", "internal", "mobile"))
	wrap := exported(t, ".")
	var problems []string
	for name, sig := range core {
		if w, ok := wrap[name]; !ok {
			problems = append(problems, "missing "+name)
		} else if w != sig {
			problems = append(problems, name+": "+w+" != "+sig)
		}
	}
	for name := range wrap {
		if _, ok := core[name]; !ok {
			problems = append(problems, "extra "+name)
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatal(strings.Join(problems, "\n"))
	}
}
