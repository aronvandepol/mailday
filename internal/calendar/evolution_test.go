package calendar

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreLoadsEvolutionCalendarCache(t *testing.T) {
	root := t.TempDir()
	dynamicRoot := filepath.Join(root, "cache", "sources")
	configRoot := filepath.Join(root, "config", "sources")
	calendarRoot := filepath.Join(root, "cache", "calendar")
	uid := "calendar-uid"
	parent := "account-uid"
	if err := os.MkdirAll(filepath.Join(dynamicRoot, parent), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(calendarRoot, uid), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	dynamic := "[Data Source]\nDisplayName=Teaching\nEnabled=true\nParent=" + parent + "\n[Calendar]\nBackendName=caldav\nSelected=true\nColor=#123abc\n"
	parentSource := "[Data Source]\nDisplayName=Account\n[Collection]\nBackendName=google\n"
	if err := os.WriteFile(filepath.Join(dynamicRoot, parent, uid+".source"), []byte(dynamic), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, parent+".source"), []byte(parentSource), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(calendarRoot, uid, "cache.db")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	store := &Store{
		CalendarCacheRoot:  calendarRoot,
		DynamicSourcesRoot: dynamicRoot,
		ConfigSourcesRoot:  configRoot,
		ICSRoot:            filepath.Join(root, "ics"),
	}
	store.query = func(_ context.Context, gotPath string) ([]cacheRow, error) {
		if gotPath != dbPath {
			t.Fatalf("db path = %q, want %q", gotPath, dbPath)
		}
		return []cacheRow{{UID: "one", Object: "BEGIN:VEVENT\nUID:one\nSUMMARY:Class\nDTSTART:20260825T100000Z\nDTEND:20260825T110000Z\nEND:VEVENT"}}, nil
	}
	from := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	result, err := store.Load(context.Background(), from, from.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(result.Events), 1; got != want {
		t.Fatalf("event count = %d, want %d", got, want)
	}
	if got, want := result.Sources[0].Provider, "Google"; got != want {
		t.Fatalf("provider = %q, want %q", got, want)
	}
}

// writeEvolutionCalendar sets up one Evolution calendar cache called name.
func writeEvolutionCalendar(t *testing.T, root, uid, name string) string {
	t.Helper()
	dynamicRoot := filepath.Join(root, "cache", "sources", "account")
	calendarRoot := filepath.Join(root, "cache", "calendar", uid)
	for _, dir := range []string{dynamicRoot, calendarRoot, filepath.Join(root, "config", "sources")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	source := "[Data Source]\nDisplayName=" + name + "\nEnabled=true\nParent=account\n[Calendar]\nBackendName=caldav\nSelected=true\n"
	if err := os.WriteFile(filepath.Join(dynamicRoot, uid+".source"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(calendarRoot, "cache.db")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func TestICSCalendarReplacesEvolutionCopyOfTheSameCalendar(t *testing.T) {
	root := t.TempDir()
	writeEvolutionCalendar(t, root, "evo-work", "Work")
	writeEvolutionCalendar(t, root, "evo-home", "Home")
	icsRoot := filepath.Join(root, "ics")
	if err := os.MkdirAll(icsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	// Work was moved from Mailday: the export has the new time, Evolution's
	// cache the old one. The export of a calendar may also be empty.
	ics := "BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:talk-1\nSUMMARY:Talk\nDTSTART:20261008T140000Z\nDTEND:20261008T150000Z\nEND:VEVENT\nEND:VCALENDAR\n"
	if err := os.WriteFile(filepath.Join(icsRoot, "Work.ics"), []byte(ics), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &Store{
		CalendarCacheRoot:  filepath.Join(root, "cache", "calendar"),
		DynamicSourcesRoot: filepath.Join(root, "cache", "sources"),
		ConfigSourcesRoot:  filepath.Join(root, "config", "sources"),
		ICSRoot:            icsRoot,
	}
	store.query = func(_ context.Context, dbPath string) ([]cacheRow, error) {
		object := "BEGIN:VEVENT\nUID:talk-1\nSUMMARY:Talk\nDTSTART:20261008T120000Z\nDTEND:20261008T130000Z\nEND:VEVENT"
		if filepath.Base(filepath.Dir(dbPath)) == "evo-home" {
			object = "BEGIN:VEVENT\nUID:dinner-1\nSUMMARY:Dinner\nDTSTART:20261008T170000Z\nDTEND:20261008T180000Z\nEND:VEVENT"
		}
		return []cacheRow{{UID: "one", Object: object}}, nil
	}
	from := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	result, err := store.Load(context.Background(), from, from.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, event := range result.Events {
		seen = append(seen, event.Summary+"@"+event.Start.UTC().Format("15:04")+"/"+event.Provider)
	}
	if want := []string{"Talk@14:00/ICS", "Dinner@17:00/CalDAV"}; len(seen) != 2 || seen[0] != want[0] || seen[1] != want[1] {
		t.Fatalf("events = %v, want %v: no ghost Talk at 12:00, Home kept", seen, want)
	}
	names := map[string]int{}
	for _, source := range result.Sources {
		names[source.Name]++
	}
	if names["Work"] != 1 || names["Home"] != 1 {
		t.Fatalf("sources = %v, want each calendar once", names)
	}
}
