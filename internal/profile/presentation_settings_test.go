package profile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openhoo/hoovda/internal/model"
)

func TestPresentationSettingsJSONIsExactAndValidated(t *testing.T) {
	settings := DefaultPresentationSettings()
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	var decoded PresentationSettings
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SpeechSymbolLevel != SpeechSymbolsSome || decoded.BrailleTether != BrailleTetherAuto {
		t.Fatalf("settings = %#v", decoded)
	}

	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "reportHeadings")
	missing, _ := json.Marshal(fields)
	if err := json.Unmarshal(missing, &decoded); err == nil {
		t.Fatal("missing field was accepted")
	}
	fields["reportHeadings"] = true
	fields["unexpected"] = true
	unknown, _ := json.Marshal(fields)
	if err := json.Unmarshal(unknown, &decoded); err == nil {
		t.Fatal("unknown field was accepted")
	}
	settings.ReportSpellingErrors = []string{SpellingErrorsSpeech, SpellingErrorsSpeech}
	if err := settings.Validate(); err == nil {
		t.Fatal("duplicate spelling channel was accepted")
	}
}

func TestPresentationSettingsControlSemanticOutput(t *testing.T) {
	presenter, _ := NewPresenter("en-US")
	settings := presenter.Settings()
	settings.ReportHeadings = false
	settings.ReportObjectPositionInformation = false
	settings.ReportObjectDescriptions = false
	settings.ReportKeyboardShortcuts = false
	settings.ReportClickable = false
	if err := presenter.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	node := &model.Node{
		Role: "heading", Name: "Checkout", Description: "Start here", HeadingLevel: 2,
		PositionInSet: 2, SetSize: 4, KeyboardShortcut: "Alt+C",
		Attributes: map[string]string{"clickable": "true"}, States: map[string]bool{"enabled": true},
	}
	got := presenter.Present(node, "focus")
	if got.Speech != "Checkout" || got.Braille != "Checkout" {
		t.Fatalf("suppressed presentation = %#v", got)
	}

	settings = DefaultPresentationSettings()
	settings.ReportTableHeaders = TableHeadersRows
	settings.ReportTableCellCoordinates = false
	if err := presenter.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	cell := &model.Node{Role: "table cell", Name: "42", Row: 2, Column: 3, RowHeaderText: []string{"Revenue"}, ColumnHeaderText: []string{"2026"}}
	got = presenter.PresentTableMove(cell, nil)
	if got.Speech != "Revenue  42" || strings.Contains(got.Braille, "r2") || strings.Contains(got.Braille, "c3") {
		t.Fatalf("table presentation = %#v", got)
	}
}

func TestPresentationSettingsCloneOwnsSpellingChannels(t *testing.T) {
	presenter, _ := NewPresenter("en-US")
	settings := presenter.Settings()
	settings.ReportSpellingErrors[0] = SpellingErrorsBraille
	if presenter.Settings().ReportSpellingErrors[0] != SpellingErrorsSpeech {
		t.Fatal("settings read leaked mutable slice")
	}
}
