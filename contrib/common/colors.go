package common

import (
	"fmt"
	"strings"
)

// ANSI color codes for terminal output
const (
	ColorReset   = "\033[0m"
	ColorRed     = "\033[31m"
	ColorGreen   = "\033[32m"
	ColorYellow  = "\033[33m"
	ColorBlue    = "\033[34m"
	ColorMagenta = "\033[35m"
	ColorCyan    = "\033[36m"
	ColorWhite   = "\033[37m"
)

// ColorCode returns the ANSI escape code for the given color name.
// Returns empty string if color is not recognized or empty.
// Supported colors: red, green, yellow, blue, magenta, cyan, white
func ColorCode(colorName string) string {
	switch strings.ToLower(strings.TrimSpace(colorName)) {
	case "red":
		return ColorRed
	case "green":
		return ColorGreen
	case "yellow":
		return ColorYellow
	case "blue":
		return ColorBlue
	case "magenta":
		return ColorMagenta
	case "cyan":
		return ColorCyan
	case "white":
		return ColorWhite
	default:
		return ""
	}
}

// Colorize wraps the text with the specified color.
// If color name is empty or not recognized, returns text unchanged.
func Colorize(text, colorName string) string {
	code := ColorCode(colorName)
	if code == "" {
		return text
	}
	return fmt.Sprintf("%s%s%s", code, text, ColorReset)
}

// ColorizeCode wraps the text with the specified ANSI color code.
// If code is empty, returns text unchanged.
func ColorizeCode(text, code string) string {
	if code == "" {
		return text
	}
	return fmt.Sprintf("%s%s%s", code, text, ColorReset)
}

