package tui

// Visual roles follow basecamp/hey-cli/internal/tui/styles.go under the MIT license.

import (
	"hash/fnv"
	"image/color"
	"math"
	"os"

	"charm.land/lipgloss/v2"

	"github.com/aronvandepol/mailday/internal/calendar"
)

var noColor = os.Getenv("NO_COLOR") != ""

var (
	colorPrimary color.Color = lipgloss.BrightBlue
	colorBright  color.Color = lipgloss.BrightWhite
	colorAlert   color.Color = lipgloss.Red
	colorLink    color.Color = lipgloss.BrightCyan
	colorError   color.Color = lipgloss.Red
	colorChrome  color.Color = lipgloss.Blue
	colorActive  color.Color = lipgloss.Yellow

	themeBackground color.Color = lipgloss.Black
	themeForeground color.Color = lipgloss.BrightWhite
	colorSelection  color.Color
	themeEventHues  = map[string]color.Color(nil)
)

var styleMuted = lipgloss.NewStyle().Faint(true)

func init() {
	applyTheme(resolveTheme())
}

func applyTheme(theme visualTheme) {
	if noColor {
		none := lipgloss.NoColor{}
		colorPrimary = none
		colorBright = none
		colorAlert = none
		colorLink = none
		colorError = none
		colorChrome = none
		colorActive = none
		themeBackground = none
		themeForeground = none
		colorSelection = nil
		themeEventHues = nil
		styleMuted = lipgloss.NewStyle()
		return
	}
	colorPrimary = theme.Accent
	colorBright = theme.Bright
	colorError = theme.Error
	colorAlert = theme.Error
	themeBackground = theme.Background
	themeForeground = theme.Bright
	colorSelection = theme.Selection
	themeEventHues = theme.Hues
	if value := theme.Hues["blue"]; value != nil {
		colorChrome = value
	}
	if value := theme.Hues["cyan"]; value != nil {
		colorLink = value
	}
	if value := theme.Hues["yellow"]; value != nil {
		colorActive = value
	}
	styleMuted = lipgloss.NewStyle().Foreground(theme.Bright).Faint(true)
}

func selectionStyle(style lipgloss.Style) lipgloss.Style {
	if colorSelection == nil {
		return style
	}
	return style.Background(colorSelection)
}

func cursorStyles() (lipgloss.Style, lipgloss.Style) {
	style := selectionStyle(lipgloss.NewStyle().Foreground(colorPrimary).Bold(true))
	return style, style
}

func eventFill(event calendar.Event) color.Color {
	roles := []string{"blue", "magenta", "cyan", "green", "yellow", "red"}
	fallback := map[string]color.Color{
		"blue":    lipgloss.Blue,
		"magenta": lipgloss.Magenta,
		"cyan":    lipgloss.Cyan,
		"green":   lipgloss.Green,
		"yellow":  lipgloss.Yellow,
		"red":     lipgloss.Red,
	}
	if noColor {
		return lipgloss.NoColor{}
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(event.Source))
	role := roles[int(hash.Sum32())%len(roles)]
	if value := themeEventHues[role]; value != nil {
		return value
	}
	return fallback[role]
}

func eventStyle(event calendar.Event, selected bool) lipgloss.Style {
	fill := eventFill(event)
	ink := color.Color(lipgloss.Black)
	if themeEventHues != nil {
		ink = themeBackground
		if contrastRatio(themeForeground, fill) > contrastRatio(themeBackground, fill) {
			ink = themeForeground
		}
	}
	style := lipgloss.NewStyle().Background(fill).Foreground(ink).Bold(true)
	if noColor {
		style = lipgloss.NewStyle().Bold(true)
	}
	if selected {
		// The selected block gets a bar down its left edge (see
		// selectionEdge) and bold text on every row, not an underline.
		style = style.Bold(true)
	}
	return style
}

func contrastRatio(first, second color.Color) float64 {
	firstLuminance := relativeLuminance(first)
	secondLuminance := relativeLuminance(second)
	if firstLuminance < secondLuminance {
		firstLuminance, secondLuminance = secondLuminance, firstLuminance
	}
	return (firstLuminance + 0.05) / (secondLuminance + 0.05)
}

func relativeLuminance(value color.Color) float64 {
	red, green, blue, _ := value.RGBA()
	channel := func(component uint32) float64 {
		normalized := float64(component) / 0xffff
		if normalized <= 0.03928 {
			return normalized / 12.92
		}
		return math.Pow((normalized+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(red) + 0.7152*channel(green) + 0.0722*channel(blue)
}

// selectionEdge starts each row of the selected event block: a solid bar in
// the block's ink colour, where other blocks have a space.
func selectionEdge(selected bool) string {
	if selected {
		return "▌"
	}
	return " "
}
