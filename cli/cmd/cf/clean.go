package main

import (
	"fmt"
	"io"
	"strings"
)

// Adversarial review, finding C1-01: the names and the texts that cf shows come from the panel,
// and a customer wrote many of them. A terminal obeys control characters, for example to hide a
// line or to change the title of the window. Thus cf shows each control character as text.

// cleanWriter shows the control characters of the output as text. A new line and a tab stay.
type cleanWriter struct{ w io.Writer }

func (c *cleanWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(c.w, clean(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// clean gives the text with each control character as text, for example "\x1b".
func clean(s string) string {
	if !strings.ContainsFunc(s, unsafeRune) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if unsafeRune(r) {
			if r < 0x100 {
				fmt.Fprintf(&b, `\x%02x`, r)
			} else {
				fmt.Fprintf(&b, `\u%04x`, r)
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// unsafeRune is true for the C0 and C1 control characters, DEL, and the characters that
// change the direction of the text or hide text.
func unsafeRune(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20 || (r >= 0x7f && r <= 0x9f):
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// oneLine gives a value for a cell of a table: a tab or a new line would make a new cell or
// a new row.
func oneLine(s string) string {
	return strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(s)
}
