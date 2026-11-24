package main

import (
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"testing"
)

func imports(t *testing.T, file string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, im := range f.Imports {
		out = append(out, im.Path.Value)
	}
	sort.Strings(out)
	return out
}

// RemoHop.exe must be able to host everything remohop can: a feature package
// added to cmd/remohop has to be added here too.
func TestSameFeaturesAsRemoHop(t *testing.T) {
	ours, theirs := imports(t, "main.go"), imports(t, "../remohop/main.go")
	// This program also imports "strings".
	var filtered []string
	for _, p := range ours {
		if p != `"strings"` {
			filtered = append(filtered, p)
		}
	}
	if !reflect.DeepEqual(filtered, theirs) {
		t.Fatalf("cmd/remohop-quick and cmd/remohop import different packages; keep the feature imports identical.\nremohop-quick: %v\nremohop:       %v", filtered, theirs)
	}
}

func TestQuickArgs(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{nil, []string{"quick"}},
		{[]string{"-psn_0_12345"}, []string{"quick"}},
		{[]string{"status"}, []string{"status"}},
		{[]string{"quick", "--no-open"}, []string{"quick", "--no-open"}},
	} {
		if got := quickArgs(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("quickArgs(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
