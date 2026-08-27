package braille

import (
	"context"
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
