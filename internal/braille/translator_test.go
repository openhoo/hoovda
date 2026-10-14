package braille

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPassthroughRejectsInvalidCursor(t *testing.T) {
	_, err := (Passthrough{}).Translate(context.Background(), "hello", "en-US", 6)
	if err == nil {
		t.Fatal("expected invalid cursor error")
	}
}

func TestCellsFromUnicodeBraille(t *testing.T) {
	cells := cellsFromUnicode("⠀⣿")
	if len(cells) != 2 || cells[0] != 0 || cells[1] != 255 {
		t.Fatalf("cells = %v", cells)
	}
}

func TestLiblouisUsesUnicodeDisplayTable(t *testing.T) {
	directory := t.TempDir()
	command := filepath.Join(directory, "lou_translate")
	script := "#!/bin/sh\ncase \"$1\" in unicode.dis,en-us-g2.ctb) printf '⠁\\n' ;; unicode.dis,de-g2.ctb) printf '⠠⠁\\n' ;; *) printf 'a\\n' ;; esac\n"
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	for locale, want := range map[string][]byte{"en-US": {1}, "de-DE": {32, 1}} {
		got, err := (Liblouis{Command: command}).Translate(context.Background(), "a", locale, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Cells, want) {
			t.Errorf("%s braille cells = %v, want dots %v", locale, got.Cells, want)
		}
	}
}
