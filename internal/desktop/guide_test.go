package desktop

import (
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The guide is one Markdown file per language (assets/guide.*.md). Both
// must have the same sections, with the same ids in the same order, and
// every link to the guide, from the guide itself or from an ⓘ of the page,
// must lead to one of them.
func TestGuide(t *testing.T) {
	read := func(name string) string {
		b, err := fs.ReadFile(assetFS, "assets/"+name)
		if err != nil {
			t.Fatal(err)
		}
		// The comment at the top describes the format; it is not shown.
		return regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(string(b), "")
	}
	heading := regexp.MustCompile(`(?m)^(#{2,3}) .*?(?:\{#([a-z0-9-]+)\})?$`)
	ids := func(md string) (secs, all []string) {
		for _, m := range heading.FindAllStringSubmatch(md, -1) {
			if m[1] == "##" && m[2] == "" {
				t.Errorf("a section without an id: %s", m[0])
			}
			if m[2] == "" {
				continue
			}
			if slices.Contains(all, m[2]) {
				t.Errorf("id %q is used twice", m[2])
			}
			all = append(all, m[2])
			if m[1] == "##" {
				secs = append(secs, m[2])
			}
		}
		return secs, all
	}
	en, zh := read("guide.en.md"), read("guide.zh.md")
	enSecs, enIDs := ids(en)
	zhSecs, zhIDs := ids(zh)
	if len(enSecs) < 8 {
		t.Fatalf("only %d sections in the English guide", len(enSecs))
	}
	if !slices.Equal(enIDs, zhIDs) {
		t.Fatalf("the guides' ids differ:\nen %v\nzh %v", enIDs, zhIDs)
	}
	if !slices.Equal(enSecs, zhSecs) {
		t.Fatalf("the guides' sections differ:\nen %v\nzh %v", enSecs, zhSecs)
	}
	link := regexp.MustCompile(`\]\(([^)\s]+)\)`)
	for name, md := range map[string]string{"en": en, "zh": zh} {
		for _, m := range link.FindAllStringSubmatch(md, -1) {
			to := m[1]
			switch {
			case strings.HasPrefix(to, "#"):
				if !slices.Contains(enIDs, to[1:]) {
					t.Errorf("guide.%s.md links to an unknown place: %s", name, to)
				}
			case strings.HasPrefix(to, "app:"):
				if !slices.Contains([]string{"computer", "connect", "sessions", "settings"}, to[4:]) {
					t.Errorf("guide.%s.md links to an unknown page of the app: %s", name, to)
				}
			case strings.HasPrefix(to, "web:"):
				if _, err := sitePage(to[4:], name); err != nil {
					t.Errorf("guide.%s.md links to a page the app does not open: %s", name, to)
				}
			default:
				t.Errorf("guide.%s.md has a link the guide cannot follow: %s", name, to)
			}
		}
	}
	// The page's ⓘ explanations and Settings link to sections by id.
	page := read("index.html")
	used := regexp.MustCompile(`data-guide="([a-z0-9-]*)"`).FindAllStringSubmatch(page, -1)
	if len(used) < 8 {
		t.Fatalf("only %d links to the guide in index.html", len(used))
	}
	for _, m := range used {
		if !slices.Contains(enIDs, m[1]) {
			t.Errorf("index.html links to an unknown place in the guide: %q", m[1])
		}
	}
}
