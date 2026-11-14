package config

import (
	"os"
	"path/filepath"
	"testing"
)

func settings() Config {
	c := Defaults()
	c.PanelURL, c.AgentToken, c.ServerID = "https://panel.example.com", "token", "01JBQ3Z5K8N6T4W2X9Y7R5P3M1"
	return c
}

// A new settings file is only for its owner: it holds the agent token.
func TestSaveMakesAFileThatOnlyTheOwnerCanRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := Save(path, settings()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, %v", fi.Mode().Perm(), err)
	}
	if c, err := Load(path); err != nil || c.AgentToken != "token" {
		t.Errorf("load = %+v, %v", c, err)
	}
}

// The agent service gives the group of its worker the right to read the file. A later save
// keeps that right, and gives no more rights.
func TestSaveKeepsTheModeOfTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := Save(path, settings()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ before, after os.FileMode }{{0o640, 0o640}, {0o600, 0o600}, {0o666, 0o640}, {0o777, 0o640}} {
		if err := os.Chmod(path, tc.before); err != nil {
			t.Fatal(err)
		}
		c := settings()
		c.GVisorPlatform = "kvm"
		if err := Save(path, c); err != nil {
			t.Fatal(err)
		}
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != tc.after {
			t.Errorf("mode after a save of a file with mode %v = %v, %v", tc.before, fi.Mode().Perm(), err)
		}
	}
	// A temporary file of a save that did not complete does not give its mode to the file.
	if err := os.WriteFile(path+".tmp", []byte("{}"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, settings()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, %v", fi.Mode().Perm(), err)
	}
}
