package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aronvandepol/mailday/internal/terminal"
)

type cacheRow struct {
	UID      string `json:"uid"`
	Object   string `json:"object"`
	Summary  string `json:"summary"`
	Location string `json:"location"`
	State    int    `json:"state"`
}

type cacheQuery func(context.Context, string) ([]cacheRow, error)

type Store struct {
	CalendarCacheRoot  string
	DynamicSourcesRoot string
	ConfigSourcesRoot  string
	ICSRoot            string
	SQLiteBin          string
	query              cacheQuery
}

func NewStore(home string) *Store {
	cacheHome := os.Getenv("XDG_CACHE_HOME")
	if cacheHome == "" {
		cacheHome = filepath.Join(home, ".cache")
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	store := &Store{
		CalendarCacheRoot:  filepath.Join(cacheHome, "evolution", "calendar"),
		DynamicSourcesRoot: filepath.Join(cacheHome, "evolution", "sources"),
		ConfigSourcesRoot:  filepath.Join(configHome, "evolution", "sources"),
		ICSRoot:            filepath.Join(dataHome, "mailday", "calendars"),
		SQLiteBin:          "sqlite3",
	}
	store.query = store.querySQLite
	return store
}

// WatchDirs lists the directories whose changes mean new calendar data: the
// ICS files mailday-calsync writes, and each Evolution calendar cache.
func (s *Store) WatchDirs() []string {
	var dirs []string
	if info, err := os.Stat(s.ICSRoot); err == nil && info.IsDir() {
		dirs = append(dirs, s.ICSRoot)
	}
	entries, _ := os.ReadDir(s.CalendarCacheRoot)
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(s.CalendarCacheRoot, entry.Name()))
		}
	}
	return dirs
}

func (s *Store) Load(ctx context.Context, from, to time.Time) (Result, error) {
	if !to.After(from) {
		return Result{}, fmt.Errorf("calendar end must follow start")
	}
	result := Result{}
	objects := make([]eventObject, 0)

	// The ICS export is kept fresh by mailday-calsync, and Mailday's own moves
	// and deletes show in it first. Where it has a calendar of the same name,
	// Evolution's older cache of that calendar is left out, or an event
	// moved or deleted here would show up again from the stale copy.
	icsObjects, icsSources, warnings := s.loadICSFiles()
	exported := make(map[string]bool, len(icsSources))
	for _, source := range icsSources {
		exported[strings.ToLower(source.Name)] = true
	}

	sources, err := s.discoverSources()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		result.Warnings = append(result.Warnings, err.Error())
	}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if exported[strings.ToLower(source.Name)] {
			continue
		}
		rows, err := s.query(ctx, source.DBPath)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s calendar cache: %v", source.Name, err))
			continue
		}
		result.Sources = append(result.Sources, source)
		for _, row := range rows {
			if row.State == 3 || strings.TrimSpace(row.Object) == "" {
				continue
			}
			objects = append(objects, eventObject{Raw: row.Object, Source: source, FallbackSummary: row.Summary, FallbackLocation: row.Location})
		}
	}

	objects = append(objects, icsObjects...)
	result.Sources = append(result.Sources, icsSources...)
	result.Warnings = append(result.Warnings, warnings...)

	events, warnings := expandObjects(objects, from, to)
	result.Events = events
	result.Warnings = append(result.Warnings, warnings...)
	sort.SliceStable(result.Sources, func(i, j int) bool { return result.Sources[i].Name < result.Sources[j].Name })
	return result, nil
}

func (s *Store) querySQLite(ctx context.Context, dbPath string) ([]cacheRow, error) {
	query := "SELECT ECacheUID AS uid, ECacheOBJ AS object, summary, location, ECacheState AS state FROM ECacheObjects WHERE ECacheState <> 3"
	command := exec.CommandContext(ctx, s.SQLiteBin, "-readonly", "-json", dbPath, query)
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			message := strings.TrimSpace(terminal.SanitizeLine(string(exitErr.Stderr)))
			if message != "" {
				return nil, errors.New(message)
			}
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(output))) == 0 {
		return nil, nil
	}
	var rows []cacheRow
	if err := json.Unmarshal(output, &rows); err != nil {
		return nil, fmt.Errorf("decode sqlite output: %w", err)
	}
	return rows, nil
}

func (s *Store) discoverSources() ([]Source, error) {
	pattern := filepath.Join(s.DynamicSourcesRoot, "*", "*.source")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		if _, err := os.Stat(s.DynamicSourcesRoot); err != nil {
			return nil, err
		}
	}
	var sources []Source
	for _, path := range paths {
		fields, err := parseSourceFile(path)
		if err != nil || fields.CalendarBackend == "" || !fields.Enabled || !fields.Selected {
			continue
		}
		uid := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		dbPath := filepath.Join(s.CalendarCacheRoot, uid, "cache.db")
		if info, err := os.Stat(dbPath); err != nil || !info.Mode().IsRegular() {
			continue
		}
		provider := fields.CalendarBackend
		if fields.Parent != "" {
			parentPath := filepath.Join(s.ConfigSourcesRoot, fields.Parent+".source")
			if parent, err := parseSourceFile(parentPath); err == nil && parent.CollectionBackend != "" {
				provider = parent.CollectionBackend
			}
		}
		name := terminal.SanitizeLine(fields.DisplayName)
		if name == "" {
			name = "Calendar"
		}
		sources = append(sources, Source{
			UID:      uid,
			Name:     name,
			Backend:  fields.CalendarBackend,
			Provider: providerLabel(provider),
			Color:    safeColor(fields.Color),
			DBPath:   dbPath,
		})
	}
	sort.SliceStable(sources, func(i, j int) bool { return sources[i].Name < sources[j].Name })
	return sources, nil
}

type sourceFields struct {
	DisplayName       string
	Parent            string
	CalendarBackend   string
	CollectionBackend string
	Color             string
	Enabled           bool
	Selected          bool
}

func parseSourceFile(path string) (sourceFields, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return sourceFields{}, err
	}
	fields := sourceFields{Enabled: true, Selected: true}
	section := ""
	for _, rawLine := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch section + "." + key {
		case "Data Source.DisplayName":
			fields.DisplayName = value
		case "Data Source.Parent":
			fields.Parent = value
		case "Data Source.Enabled":
			fields.Enabled = !strings.EqualFold(value, "false")
		case "Calendar.BackendName":
			fields.CalendarBackend = value
		case "Calendar.Color":
			fields.Color = value
		case "Calendar.Selected":
			fields.Selected = !strings.EqualFold(value, "false")
		case "Collection.BackendName":
			fields.CollectionBackend = value
		}
	}
	return fields, nil
}

func providerLabel(backend string) string {
	switch strings.ToLower(backend) {
	case "google":
		return "Google"
	case "microsoft365", "ews", "exchange":
		return "Exchange"
	case "caldav":
		return "CalDAV"
	case "local":
		return "Local"
	default:
		if backend == "" {
			return "Calendar"
		}
		return terminal.SanitizeLine(backend)
	}
}

func safeColor(value string) string {
	value = strings.TrimSpace(value)
	if len(value) != 7 || value[0] != '#' {
		return ""
	}
	for _, r := range value[1:] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return ""
		}
	}
	return value
}
