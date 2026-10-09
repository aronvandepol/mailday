package tui

// Omarchy theme loading is adapted from basecamp/hey-cli/internal/tui/theme.go under the MIT license.

import (
	"image/color"
	"os"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
)

type visualTheme struct {
	Accent     color.Color
	Selection  color.Color
	Bright     color.Color
	Error      color.Color
	Background color.Color
	Hues       map[string]color.Color
	Dark       bool
}

func defaultTheme() visualTheme {
	return visualTheme{
		Accent:     lipgloss.BrightBlue,
		Bright:     lipgloss.BrightWhite,
		Error:      lipgloss.Red,
		Background: lipgloss.Black,
		Dark:       true,
	}
}

func resolveTheme() visualTheme {
	home, _ := os.UserHomeDir()
	return resolveThemeAt(home, os.Getenv)
}

func resolveThemeAt(home string, getenv func(string) string) visualTheme {
	if getenv("NO_COLOR") != "" {
		none := lipgloss.NoColor{}
		return visualTheme{Accent: none, Bright: none, Error: none, Background: none, Dark: true}
	}
	var candidates []string
	if path := getenv("MAILDAY_THEME"); path != "" {
		candidates = append(candidates, filepath.Clean(path))
	}
	if home != "" {
		themeDir := filepath.Join(home, ".local", "state", "omarchy", "current", "theme")
		candidates = append(candidates, filepath.Join(themeDir, "hey.toml"), filepath.Join(themeDir, "colors.toml"))
	}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		return overlayTheme(defaultTheme(), parseThemeFile(string(data)))
	}
	return defaultTheme()
}

func parseThemeFile(data string) map[string]string {
	values := make(map[string]string)
	for _, raw := range strings.Split(data, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if comment := unquotedIndex(value, '#'); comment >= 0 {
			value = strings.TrimSpace(value[:comment])
		}
		value = strings.Trim(value, `"'`)
		if key != "" && value != "" {
			values[key] = value
		}
	}
	return values
}

func unquotedIndex(value string, target rune) int {
	var quote rune
	for index, current := range value {
		switch {
		case quote != 0:
			if current == quote {
				quote = 0
			}
		case current == '\'' || current == '"':
			quote = current
		case current == target:
			return index
		}
	}
	return -1
}

func overlayTheme(theme visualTheme, values map[string]string) visualTheme {
	if value, ok := themeColor(values, "accent", "bright_blue", "blue"); ok {
		theme.Accent = value
	}
	if value, ok := themeColor(values, "selection"); ok {
		theme.Selection = value
	}
	if value, ok := themeColor(values, "bright_foreground", "foreground"); ok {
		theme.Bright = value
	}
	if value, ok := themeColor(values, "error", "red"); ok {
		theme.Error = value
	}
	if value, ok := themeColor(values, "background"); ok {
		theme.Background = value
	}
	theme.Hues = themeHues(values)
	switch strings.ToLower(values["mode"]) {
	case "light":
		theme.Dark = false
	case "dark":
		theme.Dark = true
	}
	return theme
}

func themeHues(values map[string]string) map[string]color.Color {
	roles := map[string][]string{
		"blue":    {"blue"},
		"magenta": {"magenta"},
		"cyan":    {"cyan"},
		"green":   {"green"},
		"yellow":  {"yellow"},
		"red":     {"red"},
	}
	result := make(map[string]color.Color)
	for role, keys := range roles {
		if value, ok := themeColor(values, keys...); ok {
			result[role] = value
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func themeColor(values map[string]string, keys ...string) (color.Color, bool) {
	for _, key := range keys {
		value := values[key]
		if isHexColor(value) {
			return lipgloss.Color(value), true
		}
	}
	return nil, false
}

func isHexColor(value string) bool {
	if !strings.HasPrefix(value, "#") {
		return false
	}
	digits := value[1:]
	if len(digits) != 3 && len(digits) != 6 {
		return false
	}
	for _, digit := range digits {
		if (digit < '0' || digit > '9') && (digit < 'a' || digit > 'f') && (digit < 'A' || digit > 'F') {
			return false
		}
	}
	return true
}
