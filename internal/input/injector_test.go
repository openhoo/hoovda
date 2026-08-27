package input

import "testing"

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
}
