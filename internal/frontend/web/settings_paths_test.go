package web

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A person is sent to "Settings › General › Approvals" by the docs and by the
// console's own words, so each such path has to name a tab Settings has and,
// under General, a section it has. They named tabs that were folded away
// (Context, Documents & containers) and sections without their tab
// (Approvals, Context window), and nobody noticed until someone followed one.
func TestSettingsPathsNameATabAndSection(t *testing.T) {
	js, err := os.ReadFile("assets/settings.js")
	if err != nil {
		t.Fatal(err)
	}
	var tabs []string
	for _, m := range regexp.MustCompile(`\{ id: "[a-z]+", label: "([^"]+)" \}`).FindAllStringSubmatch(string(js), -1) {
		tabs = append(tabs, m[1])
	}
	if len(tabs) != 5 {
		t.Fatalf("found the tabs %q in settings.js, want its five", tabs)
	}
	// What General holds, under its tab: the approvals, what goes on while
	// nobody is at nodwyn, and the fold of what most people never change
	// with the sections inside it.
	general := []string{"Approvals", "While you're away", "Advanced", "Limits", "Context window", "Containers"}

	// A path may wrap onto the next line of a comment or a paragraph.
	ref := regexp.MustCompile("Settings ›[\\s/*#]+[_`\"“*]*([^\\n]*)")
	root := "../../.."
	var files []string
	for _, dir := range []string{"docs", "internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(path)
			if d.IsDir() {
				// The decisions are what was true when they were made, and the
				// book's sources hold copies of the code.
				if strings.Contains(rel, "/docs/adr") || strings.Contains(rel, "/docs/book/source") || strings.HasSuffix(rel, "/licenses") || strings.HasSuffix(rel, "/node_modules") {
					return filepath.SkipDir
				}
				return nil
			}
			switch filepath.Ext(path) {
			case ".md", ".tex", ".js", ".go":
				if !strings.HasSuffix(path, "_test.go") {
					files = append(files, path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	checked := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ref.FindAllStringSubmatch(string(raw), -1) {
			rest := m[1]
			tab := ""
			for _, name := range tabs {
				if strings.HasPrefix(rest, name) && (len(rest) == len(name) || !isLetter(rest[len(name)])) {
					tab = name
				}
			}
			checked++
			if tab == "" {
				t.Errorf("%s: %q after Settings › names no tab of Settings (%s)", file, firstWords(rest), strings.Join(tabs, ", "))
				continue
			}
			if tab != "General" {
				continue
			}
			after := strings.TrimSpace(strings.TrimPrefix(rest, tab))
			after, ok := strings.CutPrefix(after, "›")
			if !ok {
				continue
			}
			after = strings.TrimLeft(after, " *_`\"“")
			// The section may be on the next line of a wrapped paragraph.
			found := after == ""
			for _, name := range general {
				if strings.HasPrefix(after, name) {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: %q after Settings › General names no section of General (%s)", file, firstWords(after), strings.Join(general, ", "))
			}
		}
	}
	if checked < 20 {
		t.Fatalf("checked %d paths: the pattern no longer finds the references the docs hold", checked)
	}
}

func isLetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

func firstWords(s string) string {
	if fields := strings.Fields(s); len(fields) > 4 {
		return strings.Join(fields[:4], " ")
	}
	return strings.TrimSpace(s)
}
