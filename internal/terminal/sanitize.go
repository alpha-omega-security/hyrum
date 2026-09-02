// Package terminal makes untrusted text safe to print to a terminal while
// preserving ordinary Unicode text.
package terminal

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	bellRune              = rune(0x07)
	escapeRune            = rune(0x1b)
	deviceControlString   = rune(0x90)
	startOfString         = rune(0x98)
	controlSequence       = rune(0x9b)
	stringTerminator      = rune(0x9c)
	operatingSystemCmd    = rune(0x9d)
	privacyMessage        = rune(0x9e)
	applicationProgramCmd = rune(0x9f)
)

// SingleLine removes terminal control sequences and returns at most the first
// logical line. Tabs are rendered as spaces so a value cannot reposition the
// cursor within a status line.
func SingleLine(s string) string {
	return sanitize(s, false)
}

// Multiline removes terminal control sequences while preserving newlines and
// tabs for human-readable command output.
func Multiline(s string) string {
	return sanitize(s, true)
}

func sanitize(s string, multiline bool) string {
	runes := decodeRunes(s)
	var out strings.Builder
	for i := 0; i < len(runes); {
		r := runes[i]
		switch r {
		case escapeRune:
			i = skipEscape(runes, i+1)
			continue
		case controlSequence:
			i = skipCSI(runes, i+1)
			continue
		case deviceControlString, startOfString, operatingSystemCmd, privacyMessage, applicationProgramCmd:
			i = skipControlString(runes, i+1)
			continue
		case '\n':
			if !multiline {
				return out.String()
			}
			out.WriteRune(r)
			i++
			continue
		case '\t':
			if multiline {
				out.WriteRune(r)
			} else {
				out.WriteByte(' ')
			}
			i++
			continue
		case '\r':
			if !multiline {
				return out.String()
			}
			i++
			continue
		}

		if r == 0x7f || (r >= 0x80 && r <= 0x9f) ||
			(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) ||
			unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			i++
			continue
		}
		out.WriteRune(r)
		i++
	}
	return out.String()
}

func decodeRunes(s string) []rune {
	out := make([]rune, 0, len(s))
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 && s[0] >= 0x80 && s[0] <= 0x9f {
			// Preserve raw 8-bit C1 bytes as their control rune so the state
			// machine removes the entire sequence instead of only its prefix.
			r = rune(s[0])
		}
		out = append(out, r)
		s = s[size:]
	}
	return out
}

func skipEscape(runes []rune, i int) int {
	if i >= len(runes) {
		return i
	}
	switch runes[i] {
	case '[':
		return skipCSI(runes, i+1)
	case 'P', 'X', ']', '^', '_':
		return skipControlString(runes, i+1)
	}
	// Fe escapes have a single final byte, optionally preceded by
	// intermediate bytes in the 0x20-0x2f range.
	for i < len(runes) {
		r := runes[i]
		i++
		if r >= 0x30 && r <= 0x7e {
			break
		}
	}
	return i
}

func skipCSI(runes []rune, i int) int {
	for i < len(runes) {
		r := runes[i]
		i++
		if r >= 0x40 && r <= 0x7e {
			break
		}
	}
	return i
}

func skipControlString(runes []rune, i int) int {
	for i < len(runes) {
		switch runes[i] {
		case bellRune, stringTerminator:
			return i + 1
		case escapeRune:
			if i+1 < len(runes) && runes[i+1] == '\\' {
				return i + 2
			}
		}
		i++
	}
	return i
}
