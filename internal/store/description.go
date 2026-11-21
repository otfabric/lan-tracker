package store

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxDescription is the longest host description, in characters.
const MaxDescription = 200

// ErrDescription is returned for a description that cannot be stored.
var ErrDescription = errors.New("invalid description")

// CleanDescription trims a host description and checks it: one line of at
// most MaxDescription characters of printable text (docs/DATA_MODEL.md
// §5.8). The empty string removes a description.
func CleanDescription(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case !utf8.ValidString(s):
		return "", fmt.Errorf("%w: not UTF-8 text", ErrDescription)
	case utf8.RuneCountInString(s) > MaxDescription:
		return "", fmt.Errorf("%w: %d characters, at most %d", ErrDescription, utf8.RuneCountInString(s), MaxDescription)
	case strings.IndexFunc(s, unicode.IsControl) >= 0:
		return "", fmt.Errorf("%w: control characters (a newline, a tab) are not allowed", ErrDescription)
	case strings.IndexFunc(s, isBidiControl) >= 0:
		return "", fmt.Errorf("%w: bidirectional control characters are not allowed", ErrDescription)
	}
	return s, nil
}

// isBidiControl reports the Unicode bidirectional formatting characters,
// which change the order in which the rest of a line is displayed.
func isBidiControl(r rune) bool {
	switch {
	case r == '\u061c', r == '\u200e', r == '\u200f':
		return true
	case r >= '\u202a' && r <= '\u202e', r >= '\u2066' && r <= '\u2069':
		return true
	}
	return false
}
