package atspi

import "testing"

func TestDecodeStates(t *testing.T) {
	states := DecodeStates([]uint32{1 << 4, 1 << 0})
	if !states["checked"] || !states["indeterminate"] {
		t.Fatalf("states = %#v", states)
	}
}

func TestParseAttributesPreservesColonInValue(t *testing.T) {
	attributes := ParseAttributes([]string{"tag:h1", "url:https://example.test/a"})
	if attributes["url"] != "https://example.test/a" {
		t.Fatalf("attributes = %#v", attributes)
	}
}
