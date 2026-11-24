package xfer

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// item is one file found by walk, with its path relative to the walk root
// ("/"-separated on every platform).
type item struct {
	rel string
	e   *entry
}

// relPath joins a "/"-separated relative path onto a root of f.
func relPath(f fsys, root, rel string) string {
	if rel == "" {
		return root
	}
	if f.remote() {
		return f.join(root, rel)
	}
	return filepath.Join(root, filepath.FromSlash(rel))
}

// walk lists everything below root, skipping excluded entries (and not
// descending into excluded directories). Directories are read concurrently,
// so a remote tree costs about one round trip per directory, overlapped.
// Items come back sorted by rel, which puts every directory before its
// contents. Unreadable directories are reported in errs and skipped.
func walk(ctx context.Context, f fsys, root string, ex *Excluder, parallel int) (items []item, errs []error) {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, max(parallel, 1))
	)
	var visit func(rel string)
	visit = func(rel string) {
		defer wg.Done()
		sem <- struct{}{}
		ents, err := f.readDir(ctx, relPath(f, root, rel))
		<-sem
		if err != nil {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
			return
		}
		seen := make(map[string]bool, len(ents))
		var found []item
		for _, e := range ents {
			// Names come from a possibly hostile host: only single,
			// unique path components are accepted.
			key := dedupKey(f, e.name)
			if !safeName(e.name) || seen[key] {
				continue
			}
			seen[key] = true
			r := e.name
			if rel != "" {
				r = rel + "/" + e.name
			}
			if ex.Match(r, e.isDir()) {
				continue
			}
			found = append(found, item{rel: r, e: e})
			if e.isDir() {
				wg.Add(1)
				go visit(r)
			}
		}
		mu.Lock()
		items = append(items, found...)
		mu.Unlock()
	}
	wg.Add(1)
	go visit("")
	wg.Wait()
	sort.Slice(items, func(i, j int) bool { return items[i].rel < items[j].rel })
	return items, errs
}

// Excluder matches paths against gitignore-style patterns:
//
//   - a pattern without a slash matches a name at any depth ("*.log",
//     "node_modules");
//   - a trailing slash matches directories only ("build/");
//   - a pattern with a slash elsewhere is anchored at the transfer root
//     ("docs/*.pdf", "/vendor");
//   - "*" and "?" do not cross "/", "**" does ("**/tmp", "a/**/b"), and
//     "[...]" is a character class.
//
// Negation ("!pattern") is not supported. An excluded directory is skipped
// with everything below it.
type Excluder struct {
	pats []exPattern
}

type exPattern struct {
	re       *regexp.Regexp
	dirOnly  bool
	anchored bool
}

// NewExcluder compiles patterns. Empty patterns and "#" comments are
// ignored.
func NewExcluder(patterns []string) (*Excluder, error) {
	x := &Excluder{}
	for _, p := range patterns {
		orig := p
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		if strings.HasPrefix(p, "!") {
			return nil, fmt.Errorf("exclude %q: negation is not supported", orig)
		}
		var ep exPattern
		if strings.HasSuffix(p, "/") {
			ep.dirOnly = true
			p = strings.TrimRight(p, "/")
		}
		if strings.Contains(p, "/") {
			ep.anchored = true
			p = strings.TrimPrefix(p, "/")
		}
		if p == "" {
			return nil, fmt.Errorf("exclude %q: empty pattern", orig)
		}
		re, err := regexp.Compile("^" + globRegexp(p) + "$")
		if err != nil {
			return nil, fmt.Errorf("exclude %q: %v", orig, err)
		}
		ep.re = re
		x.pats = append(x.pats, ep)
	}
	return x, nil
}

// Match reports whether the entry at rel ("/"-separated, relative to the
// transfer root) is excluded.
func (x *Excluder) Match(rel string, isDir bool) bool {
	if x == nil {
		return false
	}
	base := path.Base(rel)
	for _, p := range x.pats {
		if p.dirOnly && !isDir {
			continue
		}
		s := base
		if p.anchored {
			s = rel
		}
		if p.re.MatchString(s) {
			return true
		}
	}
	return false
}

// globRegexp translates a glob to a regular expression body.
func globRegexp(g string) string {
	var b strings.Builder
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch c {
		case '*':
			if i+1 < len(g) && g[i+1] == '*' {
				i++
				if i+1 < len(g) && g[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			j := strings.IndexByte(g[i+1:], ']')
			if j < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := g[i+1 : i+1+j]
			i += j + 1
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			class = strings.ReplaceAll(class, `\`, `\\`)
			b.WriteString("[" + class + "]")
		case '\\':
			if i+1 < len(g) {
				i++
				b.WriteString(regexp.QuoteMeta(string(g[i])))
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}
