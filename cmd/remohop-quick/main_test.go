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

func TestLaunchArgs(t *testing.T) {
	for _, c := range []struct {
		argv0    string
		in, want []string
	}{
		{"RemoHop.exe", nil, []string{"app"}},
		{`C:\Users\a\Downloads\RemoHop-QuickSupport.exe`, nil, []string{"quick"}},
		{`C:\Users\a\Downloads\RemoHop-QuickSupport (1).exe`, nil, []string{"quick"}},
		{"/Applications/RemoHop.app/Contents/MacOS/RemoHop", []string{"-psn_0_12345"}, []string{"app"}},
		{"/Users/a/Downloads/RemoHop Quick Support.app/Contents/MacOS/RemoHop-QuickSupport", []string{"-psn_0_1"}, []string{"quick"}},
		{"./remohop-quick", nil, []string{"quick"}},
		{"/usr/local/bin/remohop-app", nil, []string{"app"}},
		{"./remohop-quick", []string{"app", "--smoke", "x"}, []string{"app", "--smoke", "x"}},
		{"RemoHop.exe", []string{"--quick"}, []string{"quick"}},
		{"RemoHop.exe", []string{"--quick", "--lang", "zh"}, []string{"quick", "--lang", "zh"}},
		{"remohop-quick", []string{"--app"}, []string{"app"}},
		{"RemoHop.exe", []string{"status"}, []string{"status"}},
	} {
		if got := launchArgs(c.argv0, c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("launchArgs(%q, %v) = %v, want %v", c.argv0, c.in, got, c.want)
		}
	}
}
