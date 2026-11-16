package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCleanShowsControlCharactersAsText(t *testing.T) {
	got := clean("shop\x1b]0;title\x07\r\x1b[2Kok\u202e\u0085 caf\u00e9\n")
	want := `shop\x1b]0;title\x07\x0d\x1b[2Kok\u202e\x85` + " caf\u00e9\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if oneLine("a\tb\nc") != "a b c" {
		t.Fatal("a cell of a table has one line and no tab")
	}
}

func TestTheOutputOfACommandHasNoControlCharacters(t *testing.T) {
	var out, errOut bytes.Buffer
	a := &app{out: &out, err: &errOut}
	if err := a.dispatch(context.Background(), []string{"version"}); err != nil {
		t.Fatal(err)
	}
	a.table([]map[string]any{{"name": "evil\x1b[2J\tforged\nrow"}}, []string{"name"})
	if strings.ContainsAny(out.String(), "\x1b\r") || strings.Count(out.String(), "\n") != 3 {
		t.Fatalf("the output has control characters or a forged row: %q", out.String())
	}
}

func TestFindRefusesANameOfTwoEngines(t *testing.T) {
	l := dbList{Prefix: "shop_"}
	rows := []map[string]any{{"id": "1", "name": "shop_main", "engine": "mariadb"}, {"id": "2", "name": "shop_main", "engine": "postgresql"}}
	if _, err := l.find(rows, "main", "database"); err == nil || !strings.Contains(err.Error(), "postgresql:main") {
		t.Fatalf("want an error that lists the engines, got %v", err)
	}
	if r, err := l.find(rows, "postgresql:main", "database"); err != nil || r["id"] != "2" {
		t.Fatalf("ENGINE:NAME selects one database, got %v %v", r, err)
	}
	if r, err := l.find(rows[:1], "main", "database"); err != nil || r["id"] != "1" {
		t.Fatalf("one match is the answer, got %v %v", r, err)
	}
	for name := range map[string]bool{"env": true, "mail": true, "notifications": true, "mail-server": true} {
		if !builtin[name] {
			t.Fatalf("%s is a command of cf. A plugin must not replace it", name)
		}
	}
}
