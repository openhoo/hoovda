package input

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeForXDoTool(t *testing.T) {
	if got := normalizeForXDoTool("ctrl+alt+right"); got != "ctrl+alt+Right" {
		t.Fatalf("got %q", got)
	}
	if got := normalizeForXDoTool("insert+space"); got != "Insert+space" {
		t.Fatalf("got %q", got)
	}
	if got := normalizeForXDoTool("insert+f7"); got != "Insert+F7" {
		t.Fatalf("got %q", got)
	}
	if got := normalizeForXDoTool("f6"); got != "F6" {
		t.Fatalf("got %q", got)
	}
	if got := normalizeForXDoTool("shift+numpad2"); got != "shift+KP_Down" {
		t.Fatalf("got %q", got)
	}
	if got := normalizeForXDoTool("shift+,"); got != "shift+comma" {
		t.Fatalf("got %q", got)
	}
	if got := normalizeForXDoTool(","); got != "comma" {
		t.Fatalf("got %q", got)
	}
}

func TestVirtualModifierChordUsesExplicitPressAndReverseRelease(t *testing.T) {
	directory := t.TempDir()
	logPath := filepath.Join(directory, "calls")
	command := filepath.Join(directory, "xdotool")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HOOVDA_INPUT_TEST_LOG\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOOVDA_INPUT_TEST_LOG", logPath)
	injector := XDoTool{Command: command}
	if err := injector.Press(context.Background(), "capslock+shift+s"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "keydown Caps_Lock\nkeydown shift\nkey s\nkeyup shift\nkeyup Caps_Lock\n"
	if strings.ReplaceAll(string(got), "\r\n", "\n") != want {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}
