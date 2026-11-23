// Package termlog records a terminal session of a website: the command lines that the user
// runs, and the output of the session.
//
// The agent does not record the keys that the user types, because a password prompt does not
// show them. It finds each command line in the output instead: when the user presses Enter,
// the line that the terminal shows is the prompt and the command. Tracker keeps that line
// with a small part of a VT100 terminal: the characters, the cursor moves and the erase
// sequences that a shell uses for line editing. The prompt is the start of the line when the
// user starts to type. A line that the terminal does not show, for example a password, gives
// no command.
//
// A full-screen program, for example an editor, uses the alternate screen. Tracker records no
// commands while the alternate screen is in use. The recording of the output (Recorder) has
// all that the terminal showed.
package termlog

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxCommand is the longest command line that Tracker keeps, in characters.
const MaxCommand = 2000

// maxLine is the longest line that Tracker follows.
const maxLine = 8192

// maxPending is the number of Enter keys that wait for the end of their line in the output.
const maxPending = 4

// Tracker finds the command lines of a terminal session. It is not safe for use by more
// than one goroutine.
//
// Tracker follows one logical line: the rows that the terminal joins when a line is longer
// than the width of the terminal. col is the position of the cursor in the logical line.
type Tracker struct {
	// Command receives each command line.
	Command func(line string)

	width int
	line  []rune
	col   int
	// wrap is true when the cursor is at the right margin after a character, and the next
	// character goes to the next row (the "pending wrap" of xterm).
	wrap bool
	alt  bool

	// typing is true after the first key of a line. prompt is the line when the user started
	// to type.
	typing bool
	prompt []rune
	// pending are the lines that the user ended with Enter, and that the output did not end
	// yet.
	pending []pendingLine

	// The state of the escape sequence parser.
	state  int
	params []byte
	utf    []byte
}

// pendingLine is a line that the user ended with Enter.
type pendingLine struct {
	prompt []rune
	// line is the line when the user pressed Enter.
	line []rune
	// queued is true when the user pressed Enter while an earlier line waited, for example
	// for a paste of more lines. The output of the earlier command comes first, so the line
	// is the next line that starts with the prompt.
	queued bool
	// skipped counts the lines of output that did not start with the prompt.
	skipped int
}

// maxSkipped is the number of lines of output that a queued line waits for its prompt.
const maxSkipped = 1000

// NewTracker gives a tracker for a terminal of a width in characters.
func NewTracker(cols int, command func(string)) *Tracker {
	t := &Tracker{Command: command}
	t.Resize(cols)
	return t
}

// Resize gives the new width of the terminal.
func (t *Tracker) Resize(cols int) {
	t.width = min(max(cols, 10), 1000)
}

// The states of the parser.
const (
	ground = iota
	escape
	csi
	str     // OSC, DCS, SOS, PM and APC: a string to the string terminator
	strEsc  // ESC in a string: the start of the string terminator
	charset // ESC ( and similar: one more byte
)

// Input gives the keys that the user typed.
func (t *Tracker) Input(p []byte) {
	for _, b := range p {
		if t.alt {
			continue
		}
		if !t.typing {
			// An Enter that the output did not end with a new line, for example the Enter of
			// a password with no echo: the output changed the line after it.
			if len(t.pending) > 0 && !t.pending[0].queued && string(t.line) != string(t.pending[0].line) {
				t.pending = t.pending[1:]
			}
			t.prompt = t.snapshot()
			t.typing = true
		}
		if b == '\r' || b == '\n' {
			if len(t.pending) == maxPending {
				t.pending = t.pending[1:]
			}
			t.pending = append(t.pending, pendingLine{prompt: t.prompt, line: append([]rune(nil), t.line...), queued: len(t.pending) > 0})
			t.typing = false
		}
	}
}

// snapshot gives the line to the cursor.
func (t *Tracker) snapshot() []rune {
	n := min(t.col, len(t.line))
	return append([]rune(nil), t.line[:n]...)
}

// Output gives the output of the terminal.
func (t *Tracker) Output(p []byte) {
	if len(t.utf) > 0 {
		p = append(t.utf, p...)
		t.utf = nil
	}
	for i := 0; i < len(p); {
		b := p[i]
		if t.state != ground || b < 0x80 {
			t.byte(b)
			i++
			continue
		}
		if !utf8.FullRune(p[i:]) {
			t.utf = append([]byte(nil), p[i:]...)
			return
		}
		r, n := utf8.DecodeRune(p[i:])
		t.put(r)
		i += n
	}
}

// rowStart gives the position of the first character of the row of the cursor.
func (t *Tracker) rowStart() int {
	if t.wrap {
		return t.col - t.width
	}
	return t.col / t.width * t.width
}

// lastRow reports whether the row of the cursor is the last row of the line.
func (t *Tracker) lastRow() bool { return t.rowStart()+t.width >= len(t.line) }

// move sets the cursor in the logical line.
func (t *Tracker) move(col int) {
	t.col = min(max(col, 0), maxLine)
	t.wrap = false
}

func (t *Tracker) byte(b byte) {
	switch t.state {
	case escape:
		t.state = ground
		switch b {
		case '[':
			t.state, t.params = csi, t.params[:0]
		case ']', 'P', 'X', '^', '_':
			t.state = str
		case '(', ')', '*', '+', '-', '.', '/', '#', '%', ' ':
			t.state = charset
		case 'c':
			t.line = nil
			t.move(0)
		}
		return
	case charset:
		t.state = ground
		return
	case str:
		switch b {
		case 0x07:
			t.state = ground
		case 0x1b:
			t.state = strEsc
		}
		return
	case strEsc:
		t.state = str
		if b == '\\' {
			t.state = ground
		}
		return
	case csi:
		if b >= 0x40 && b <= 0x7e {
			t.state = ground
			t.csi(b)
		} else if len(t.params) < 64 {
			t.params = append(t.params, b)
		}
		return
	}
	switch {
	case b == 0x1b:
		t.state = escape
	case b == '\r':
		t.move(t.rowStart())
	case b == '\n' || b == '\v' || b == '\f':
		t.newline()
	case b == '\b':
		if t.wrap {
			t.move(t.col - 2)
		} else if t.col > t.rowStart() {
			t.move(t.col - 1)
		}
	case b == '\t':
		start := t.rowStart()
		t.move(min(start+t.width-1, start+((t.col-start)/8+1)*8))
	case b >= 0x20 && b < 0x7f:
		t.put(rune(b))
	}
}

// put writes a character at the cursor.
func (t *Tracker) put(r rune) {
	if t.col >= maxLine {
		return
	}
	t.wrap = false
	for len(t.line) < t.col {
		t.line = append(t.line, ' ')
	}
	if t.col < len(t.line) {
		t.line[t.col] = r
	} else {
		t.line = append(t.line, r)
	}
	t.col++
	t.wrap = t.col%t.width == 0
}

// newline moves the cursor down. When the cursor is on the last row of the line, the line
// ends. A line that the user ended with Enter is a command.
func (t *Tracker) newline() {
	if t.wrap {
		t.wrap = false
		if t.col < len(t.line) {
			return
		}
	} else if !t.lastRow() {
		t.move(t.col + t.width)
		return
	}
	if len(t.pending) > 0 && !t.alt {
		p := &t.pending[0]
		if !p.queued || len(p.prompt) == 0 || t.startsWith(p.prompt) {
			t.commit(p.prompt)
			t.pending = t.pending[1:]
		} else if p.skipped++; p.skipped > maxSkipped {
			t.pending = t.pending[1:]
		}
	}
	t.line = nil
	t.move(0)
	// A key that the user typed before the line ended was on this line. The next key starts
	// a new line.
	t.typing = false
}

// startsWith reports whether the line starts with the prompt.
func (t *Tracker) startsWith(prompt []rune) bool {
	return len(prompt) <= len(t.line) && string(t.line[:len(prompt)]) == string(prompt)
}

func (t *Tracker) commit(prompt []rune) {
	line := t.line
	if t.startsWith(prompt) {
		line = line[len(prompt):]
	}
	command := strings.TrimSpace(string(line))
	if command == "" || t.Command == nil {
		return
	}
	if utf8.RuneCountInString(command) > MaxCommand {
		command = string([]rune(command)[:MaxCommand])
	}
	t.Command(command)
}

// n gives the numeric parameter i of the sequence, or def when it is not there or 0.
func (t *Tracker) n(i, def int) int {
	parts := strings.Split(strings.TrimLeft(string(t.params), "?>=<"), ";")
	if i >= len(parts) {
		return def
	}
	v, err := strconv.Atoi(parts[i])
	if err != nil || v <= 0 {
		return def
	}
	return min(v, maxLine)
}

// blank writes spaces from position from to position to, in the line.
func (t *Tracker) blank(from, to int) {
	for i := max(from, 0); i < to && i < len(t.line); i++ {
		t.line[i] = ' '
	}
}

func (t *Tracker) csi(final byte) {
	private := len(t.params) > 0 && t.params[0] == '?'
	start := t.rowStart()
	end := start + t.width
	switch final {
	case 'A':
		t.move(max(t.col-t.n(0, 1)*t.width, t.col%t.width))
	case 'B':
		t.move(t.col + t.n(0, 1)*t.width)
	case 'C':
		t.move(min(end-1, t.col+t.n(0, 1)))
	case 'D':
		col := t.col
		if t.wrap {
			col--
		}
		t.move(max(start, col-t.n(0, 1)))
	case 'G', '`':
		t.move(start + min(t.n(0, 1), t.width) - 1)
	case 'H', 'f':
		t.move(start + min(t.n(1, 1), t.width) - 1)
	case 'K':
		from, to := t.col, end
		switch t.n(0, 0) {
		case 1:
			from, to = start, t.col+1
		case 2:
			from = start
		}
		if to >= end && t.lastRow() {
			t.blank(from, to)
			if from < len(t.line) {
				t.line = t.line[:from]
			}
			return
		}
		t.blank(from, to)
	case 'J':
		switch t.n(0, 0) {
		case 0:
			if t.col < len(t.line) {
				t.line = t.line[:t.col]
			}
		case 2, 3:
			t.line = nil
			t.move(0)
		}
	case 'P':
		if t.col >= len(t.line) {
			return
		}
		rowEnd := min(end, len(t.line))
		n := min(t.n(0, 1), rowEnd-t.col)
		row := append(append([]rune(nil), t.line[t.col+n:rowEnd]...), []rune(strings.Repeat(" ", n))...)
		if t.lastRow() {
			row = row[:len(row)-n]
		}
		t.line = append(append(t.line[:t.col:t.col], row...), t.line[rowEnd:]...)
	case 'X':
		t.blank(t.col, min(end, t.col+t.n(0, 1)))
	case '@':
		if t.col >= len(t.line) {
			return
		}
		rowEnd := min(end, len(t.line))
		n := t.n(0, 1)
		row := append([]rune(strings.Repeat(" ", n)), t.line[t.col:rowEnd]...)
		if keep := end - t.col; len(row) > keep {
			row = row[:keep]
		}
		t.line = append(append(t.line[:t.col:t.col], row...), t.line[rowEnd:]...)
	case 'h', 'l':
		if !private {
			return
		}
		for _, p := range strings.Split(string(t.params[1:]), ";") {
			if p == "1049" || p == "1047" || p == "47" {
				t.alt = final == 'h'
				t.pending, t.typing = nil, false
			}
		}
	}
}

// Recorder keeps the output of a session in the asciicast format, version 2. It keeps the
// output to a size limit, and then marks the recording as cut.
type Recorder struct {
	start time.Time
	max   int
	buf   []byte
	cut   bool
	utf   []byte
}

// NewRecorder starts a recording of a terminal with a size in characters.
func NewRecorder(start time.Time, cols, rows, max int) *Recorder {
	head, _ := json.Marshal(map[string]any{
		"version": 2, "width": cols, "height": rows, "timestamp": start.Unix(),
		"env": map[string]string{"TERM": "xterm-256color"},
	})
	return &Recorder{start: start, max: max, buf: append(head, '\n')}
}

// Output adds output of the terminal. A character that the chunk cuts in two waits for the
// next chunk.
func (r *Recorder) Output(at time.Time, p []byte) {
	if len(r.utf) > 0 {
		p = append(r.utf, p...)
		r.utf = nil
	}
	if n := incomplete(p); n > 0 {
		r.utf = append([]byte(nil), p[len(p)-n:]...)
		p = p[:len(p)-n]
	}
	if len(p) > 0 {
		r.event(at, "o", string(p))
	}
}

// Resize records a new size of the terminal.
func (r *Recorder) Resize(at time.Time, cols, rows int) {
	r.event(at, "r", fmt.Sprintf("%dx%d", cols, rows))
}

func (r *Recorder) event(at time.Time, kind, data string) {
	if r.cut {
		return
	}
	line, _ := json.Marshal([]any{json.Number(strconv.FormatFloat(at.Sub(r.start).Seconds(), 'f', 6, 64)), kind, data})
	if len(r.buf)+len(line)+1 > r.max {
		r.cut = true
		return
	}
	r.buf = append(append(r.buf, line...), '\n')
}

// Recording gives the recording, and true when the recording reached the size limit.
func (r *Recorder) Recording() ([]byte, bool) { return r.buf, r.cut }

// incomplete gives the number of bytes at the end of p that are the start of a UTF-8
// character with no end.
func incomplete(p []byte) int {
	for n := 1; n <= 3 && n <= len(p); n++ {
		b := p[len(p)-n]
		if b < 0x80 {
			return 0
		}
		if b >= 0xc0 {
			if utf8.FullRune(p[len(p)-n:]) {
				return 0
			}
			return n
		}
	}
	return 0
}
