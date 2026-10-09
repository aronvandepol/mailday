package files

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWordsMatchAnywhereAndFileNamesRankFirst(t *testing.T) {
	home := t.TempDir()
	for path, age := range map[string]time.Duration{
		"Documents/1_projects/KJG26/abstract_conf.pdf":          24 * time.Hour,
		"Documents/1_projects/conf/abstract-old.pdf":            900 * 24 * time.Hour,
		"Documents/1_projects/KJG26/abstract_v4.pdf":            24 * time.Hour,
		"Documents/proj/node_modules/conf/abstract.pdf":         time.Hour,
		"Documents/.hidden/conf-abstract.pdf":                   time.Hour,
		"Downloads/ESS conference programme abstracts 2019.pdf": 400 * 24 * time.Hour,
		"Downloads/conf-abstract.pdf":                           24 * time.Hour,
		"Pictures/conf-abstract.png":                            time.Hour,
	} {
		full := filepath.Join(home, path)
		_ = os.MkdirAll(filepath.Dir(full), 0o700)
		_ = os.WriteFile(full, []byte("x"), 0o600)
		when := time.Now().Add(-age)
		_ = os.Chtimes(full, when, when)
	}
	ctx := context.Background()
	got := rank(walk(ctx, Roots(home), "abstract"), []string{"conf", "abstract"}, home, 10)
	var names []string
	for _, hit := range got {
		names = append(names, filepath.Base(hit.Path))
	}
	// Documents ranks first; the Downloads twin follows; Pictures is not searched.
	want := []string{"abstract_conf.pdf", "conf-abstract.pdf", "abstract-old.pdf", "ESS conference programme abstracts 2019.pdf"}
	if len(names) != len(want) {
		t.Fatalf("hits = %v, want %v (node_modules, hidden folders and non-matches left out)", names, want)
	}
	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("hits = %v, want %v", names, want)
		}
	}
	if hits := Search(ctx, home, "kjg26 v4", 5); len(hits) != 1 || filepath.Base(hits[0].Path) != "abstract_v4.pdf" {
		t.Fatalf("folder words must count too: %+v", hits)
	}
}
