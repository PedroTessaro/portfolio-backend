package svgterm

import "strings"

type Palette struct {
	Name       string
	Background string
	TitleBar   string
	Border     string
	Foreground string
	Dim        string
	Green      string
	Cyan       string
	Blue       string
	Purple     string
	Orange     string
	Yellow     string
	Red        string
}

var dark = Palette{
	Name:       "dark",
	Background: "#1a1b26",
	TitleBar:   "#16161e",
	Border:     "#292e42",
	Foreground: "#c0caf5",
	Dim:        "#565f89",
	Green:      "#9ece6a",
	Cyan:       "#7dcfff",
	Blue:       "#7aa2f7",
	Purple:     "#bb9af7",
	Orange:     "#ff9e64",
	Yellow:     "#e0af68",
	Red:        "#f7768e",
}

// Tokyo Night Day: same roles, darkened enough to stay readable on a light
// background.
var light = Palette{
	Name:       "light",
	Background: "#e1e2e7",
	TitleBar:   "#d6d8df",
	Border:     "#b6b9c8",
	Foreground: "#3760bf",
	Dim:        "#7a80a4",
	Green:      "#587539",
	Cyan:       "#007197",
	Blue:       "#2e7de9",
	Purple:     "#7847bd",
	Orange:     "#b15c00",
	Yellow:     "#8c6c3e",
	Red:        "#c64343",
}

// ThemeByName resolves the ?theme= parameter, falling back to dark.
func ThemeByName(name string) Palette {
	if strings.EqualFold(strings.TrimSpace(name), "light") {
		return light
	}
	return dark
}
