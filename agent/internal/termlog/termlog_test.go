package termlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

const prompt = "acme_x7k2@web:/app$ "

// session is a terminal of 80 characters that records the commands.
type session struct {
	t        *Tracker
	commands []string
}

func newSession(width int) *session {
	s := &session{}
	s.t = NewTracker(width, func(c string) { s.commands = append(s.commands, c) })
	s.out(prompt)
	return s
}

func (s *session) in(keys string)  { s.t.Input([]byte(keys)) }
func (s *session) out(text string) { s.t.Output([]byte(text)) }

// typeEcho types the text one key at a time, and the shell shows each key.
func (s *session) typeEcho(text string) {
	for _, r := range text {
		s.in(string(r))
		s.out(string(r))
	}
}

// enter presses Enter, and the shell shows the output of the command and a new prompt.
func (s *session) enter(output string) {
	s.in("\r")
	s.out("\r\n" + output + prompt)
}

func (s *session) want(t *testing.T, commands ...string) {
	t.Helper()
	if !slices.Equal(s.commands, commands) {
		t.Fatalf("got %q, want %q", s.commands, commands)
	}
}

func TestACommandIsTheLineAfterThePrompt(t *testing.T) {
	s := newSession(80)
	s.typeEcho("ls -la")
	s.enter("total 0\r\ndrwxr-xr-x 2 acme acme 40 .\r\n")
	// Enter with no command gives no entry.
	s.enter("")
	s.typeEcho("  php artisan migrate --force  ")
	s.enter("Migrated.\r\n")
	s.want(t, "ls -la", "php artisan migrate --force")
}

func TestLineEditingChangesTheCommand(t *testing.T) {
	s := newSession(80)
	// Backspace: readline moves back and erases to the end of the line.
	s.typeEcho("lx")
	s.in("\x7f")
	s.out("\b\x1b[K")
	s.typeEcho("s")
	// Left twice, insert a character, as readline shows it.
	s.in("\x1b[D\x1b[D")
	s.out("\b\b")
	s.in("e")
	s.out("\x1b[1@e")
	s.enter("")
	// Delete a character in the middle: readline sends DCH.
	s.typeEcho("catt file")
	s.in("\x1b[D\x1b[D\x1b[D\x1b[D\x1b[D\x1b[D")
	s.out("\x1b[6D")
	s.in("\x1b[3~")
	s.out("\x1b[P")
	s.enter("")
	s.want(t, "els", "cat file")
}

func TestCtrlCDiscardsTheLine(t *testing.T) {
	s := newSession(80)
	s.typeEcho("rm -rf storage")
	s.in("\x03")
	s.out("^C\r\n" + prompt)
	s.typeEcho("pwd")
	s.enter("/app\r\n")
	s.want(t, "pwd")
}

func TestAPasswordThatTheTerminalDoesNotShowGivesNoCommand(t *testing.T) {
	s := newSession(80)
	s.typeEcho("mysql -u shop -p")
	s.in("\r")
	s.out("\r\nEnter password: ")
	// The program turns the echo off: the output does not show the keys.
	s.in("secret")
	s.in("\r")
	s.out("\r\nWelcome to the MariaDB monitor.\r\n")
	s.want(t, "mysql -u shop -p")
}

func TestHistoryAndCompletionUseTheLineOnTheScreen(t *testing.T) {
	s := newSession(80)
	// Arrow up: readline writes the command from the history.
	s.in("\x1b[A")
	s.out("composer install")
	s.enter("")
	// Tab: readline completes the word.
	s.typeEcho("php art")
	s.in("\t")
	s.out("isan ")
	s.typeEcho("about")
	s.enter("")
	// Ctrl-L clears the screen and shows the prompt and the line again.
	s.typeEcho("whoami")
	s.in("\x0c")
	s.out("\x1b[H\x1b[2J" + prompt + "whoami")
	s.enter("acme_x7k2\r\n")
	s.want(t, "composer install", "php artisan about", "whoami")
}

func TestALongCommandWrapsToTheNextRow(t *testing.T) {
	s := newSession(30)
	long := "wp search-replace http://old.example.com https://new.example.com --all-tables"
	s.typeEcho(long)
	s.enter("")
	// Readline writes a space and a carriage return at the margin with some terminals.
	s.in("x")
	cmd := strings.Repeat("y", 30-len(prompt)-1)
	s.out("x" + cmd[:len(cmd)])
	s.out(" \r")
	s.typeEcho("zz")
	s.enter("")
	s.want(t, long, "x"+cmd+"zz")
}

func TestAFullScreenProgramGivesNoCommands(t *testing.T) {
	s := newSession(80)
	s.typeEcho("vim .env")
	s.in("\r")
	s.out("\r\n\x1b[?1049h\x1b[H\x1b[2J~\r\n~\r\n")
	s.in(":wq\r")
	s.out("\r\n\"x\" written\r\n")
	s.out("\x1b[?1049l" + prompt)
	s.typeEcho("ls")
	s.enter("")
	s.want(t, "vim .env", "ls")
}

func TestPastedLinesWaitForTheirPrompt(t *testing.T) {
	s := newSession(80)
	s.in("echo one\recho two\r")
	s.out("echo one\r\none\r\n" + prompt + "echo two\r\ntwo\r\n" + prompt)
	s.want(t, "echo one", "echo two")
}

func TestOutputWithEscapeSequencesAndSplitCharacters(t *testing.T) {
	s := newSession(80)
	s.in("e")
	// Colours, a title (OSC) and a character set change are not part of the line.
	s.out("\x1b]0;acme: /app\x07\x1b[1;32me\x1b(B\x1b[m")
	s.typeEcho("cho café")
	// The two bytes of "é" in two chunks.
	s.in("\r")
	s.out("\r\ncaf\xc3")
	s.out("\xa9\r\n" + prompt)
	s.want(t, "echo café")
}

func TestARecordingIsAnAsciicast(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	r := NewRecorder(start, 120, 30, 400)
	r.Output(start.Add(time.Second), []byte("hello \xc3"))
	r.Output(start.Add(1500*time.Millisecond), []byte("\xa9\r\n"))
	r.Resize(start.Add(2*time.Second), 100, 40)
	rec, cut := r.Recording()
	if cut {
		t.Fatal("the recording is not at its limit")
	}
	lines := bytes.Split(bytes.TrimSpace(rec), []byte("\n"))
	var head map[string]any
	if err := json.Unmarshal(lines[0], &head); err != nil || head["version"] != 2.0 || head["width"] != 120.0 || head["height"] != 30.0 {
		t.Fatalf("header %s: %v", lines[0], err)
	}
	want := []string{`[1.000000,"o","hello "]`, `[1.500000,"o","é\r\n"]`, `[2.000000,"r","100x40"]`}
	for i, w := range want {
		if got := string(lines[i+1]); got != w {
			t.Fatalf("event %d: got %s, want %s", i, got, w)
		}
	}
	// The recording stops at its limit.
	r.Output(start.Add(3*time.Second), bytes.Repeat([]byte("x"), 500))
	r.Output(start.Add(4*time.Second), []byte("y"))
	rec, cut = r.Recording()
	if !cut || len(rec) > 400 {
		t.Fatalf("cut %v, %d bytes", cut, len(rec))
	}
	sc := bufio.NewScanner(bytes.NewReader(rec))
	n := 0
	for sc.Scan() {
		n++
	}
	if n != 4 {
		t.Fatalf("got %d lines, want the header and 3 events", n)
	}
}
