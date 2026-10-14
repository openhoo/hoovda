package profile

import (
	"encoding/json"
	"testing"

	"github.com/openhoo/hoovda/internal/model"
)

func TestCharacterPresentationNamesWhitespace(t *testing.T) {
	for _, locale := range []string{"en-US", "de-DE"} {
		presenter, err := NewPresenter(locale)
		if err != nil {
			t.Fatal(err)
		}
		words := map[string]string{" ": "space", "\t": "tab", "\n": "new line", "\r": "new line"}
		if locale == "de-DE" {
			words = map[string]string{" ": "Leerzeichen", "\t": "Tabulator", "\n": "neue Zeile", "\r": "neue Zeile"}
		}
		for character, want := range words {
			if got := presenter.PresentText(nil, character, model.TextUnitCharacter); got.Speech != want || got.Braille != character {
				t.Errorf("%s %q presentation = %#v, want %q", locale, character, got, want)
			}
		}
	}
}

func TestPresentationSettingsRejectNullFields(t *testing.T) {
	encoded, err := json.Marshal(DefaultPresentationSettings())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for field, original := range fields {
		fields[field] = json.RawMessage("null")
		input, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		var settings PresentationSettings
		if err := json.Unmarshal(input, &settings); err == nil {
			t.Errorf("null %s accepted as %#v", field, settings)
		}
		fields[field] = original
	}
}

func TestNormalizeGestureKeepsMinusKey(t *testing.T) {
	for input, want := range map[string]string{
		"-": "-", "insert+-": "insert+-", "ctrl+-": "ctrl+-", "Control-Shift-F": "ctrl+shift+f",
	} {
		if got := NormalizeGesture(input); got != want {
			t.Errorf("NormalizeGesture(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPresentationSettingsRejectDuplicateKeys(t *testing.T) {
	encoded, err := json.Marshal(DefaultPresentationSettings())
	if err != nil {
		t.Fatal(err)
	}
	input := append([]byte(`{"reportHeadings":false,`), encoded[1:]...)
	var decoded PresentationSettings
	if err := json.Unmarshal(input, &decoded); err == nil {
		t.Fatal("ambiguous duplicate presentation flag accepted")
	}
}
