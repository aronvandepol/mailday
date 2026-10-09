package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveThemeReadsCurrentOmarchyColors(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, ".local", "state", "omarchy", "current", "theme")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	data := `mode = "light"
accent = "#123456"
background = "#fefefe"
bright_foreground = "#101010"
red = "#aa0000"
blue = "#0000aa"
cyan = "#008888"
yellow = "#888800"
`
	if err := os.WriteFile(filepath.Join(directory, "colors.toml"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	theme := resolveThemeAt(home, func(string) string { return "" })
	if theme.Dark {
		t.Fatal("theme mode is dark, want light")
	}
	red, green, blue, _ := theme.Accent.RGBA()
	if red>>8 != 0x12 || green>>8 != 0x34 || blue>>8 != 0x56 {
		t.Fatalf("accent RGB = %02x%02x%02x", red>>8, green>>8, blue>>8)
	}
	if theme.Hues["cyan"] == nil {
		t.Fatal("cyan event hue was not loaded")
	}
}

func TestParseThemeFileKeepsHashInsideQuotes(t *testing.T) {
	values := parseThemeFile("accent = \"#abcdef\" # comment\n")
	if got, want := values["accent"], "#abcdef"; got != want {
		t.Fatalf("accent = %q, want %q", got, want)
	}
}
