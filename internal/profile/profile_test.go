package profile

import (
	"strings"
	"testing"

	"github.com/openhoo/hoovda/internal/model"
)

func TestCatalogIsUnambiguous(t *testing.T) {
	if err := ValidateCatalog(); err != nil {
		t.Fatal(err)
	}
}

func TestEnglishHeadingPresentation(t *testing.T) {
	presenter, _ := NewPresenter("en-US")
	got := presenter.Present(&model.Node{Role: "heading", Name: "Checkout", HeadingLevel: 1, States: map[string]bool{"enabled": true}}, "quickNavigation")
	if got.Speech != "Checkout  heading  level 1" || got.Braille != "Checkout h1" {
		t.Fatalf("speech = %q", got.Speech)
	}
}

func TestGermanCheckboxPresentation(t *testing.T) {
	presenter, _ := NewPresenter("de-DE")
	got := presenter.Present(&model.Node{Role: "check box", Name: "AGB", States: map[string]bool{"enabled": true, "checked": true}}, "focus")
	if !strings.Contains(got.Speech, "Kontrollkästchen  aktiviert") {
		t.Fatalf("speech = %q", got.Speech)
	}
}

func TestEnglishLandmarkPresentationUsesSemanticRole(t *testing.T) {
	presenter, _ := NewPresenter("en-US")
	got := presenter.Present(&model.Node{Role: "landmark", Attributes: map[string]string{"xml-roles": "main"}, States: map[string]bool{"enabled": true}}, "quickNavigation")
	if got.Speech != "main landmark" {
		t.Fatalf("speech = %q", got.Speech)
	}
}

func TestTablePresentationIncludesDimensions(t *testing.T) {
	presenter, _ := NewPresenter("en-US")
	got := presenter.Present(&model.Node{Role: "table", Name: "Rates", RowCount: 3, ColumnCount: 2, States: map[string]bool{"enabled": true}}, "quickNavigation")
	if got.Speech != "Rates  table  with 3 rows and 2 columns" || got.Braille != "Rates tbl 3r 2c" {
		t.Fatalf("speech = %q", got.Speech)
	}
}

func TestTableCellPresentationIncludesHeadersAndSpans(t *testing.T) {
	presenter, _ := NewPresenter("en-US")
	got := presenter.Present(&model.Node{
		Role:             "table cell",
		Name:             "42",
		ColumnHeaderText: []string{"Year"},
		RowHeaderText:    []string{"Revenue"},
		Row:              2,
		Column:           2,
		ColumnSpan:       2,
		States:           map[string]bool{"enabled": true},
	}, "tableNavigation")
	if got.Speech != "row 2  column 2  Year  Revenue  42  spans 2 columns" {
		t.Fatalf("speech = %q", got.Speech)
	}
}

func TestPresentationIncludesDescriptionAndRelevantRelationsOnce(t *testing.T) {
	presenter, _ := NewPresenter("en-US")
	got := presenter.Present(&model.Node{
		Role:        "entry",
		Name:        "Email",
		Description: "Work address",
		States:      map[string]bool{"enabled": true, "invalid": true},
		RelationText: map[string][]string{
			"described by":  {"Work address"},
			"details":       {"Privacy details"},
			"error message": {"Enter a valid email"},
		},
	}, "focus")
	if got.Speech != "Email  entry  invalid entry  Work address  has details  Enter a valid email" {
		t.Fatalf("speech = %q", got.Speech)
	}
}

func TestDetailsAreAnnouncedWithoutLeakingTargetContent(t *testing.T) {
	presenter, _ := NewPresenter("en-US")
	got := presenter.Present(&model.Node{
		Role: "push button", Name: "push me", States: map[string]bool{"enabled": true},
		RelationText: map[string][]string{"details": {"Press to self-destruct"}},
	}, "focus")
	if got.Speech != "push me  button  has details" || got.Braille != "push me btn details" {
		t.Fatalf("presentation = %#v", got)
	}
	details := presenter.Details([]string{"Press to self-destruct"})
	if details.Speech != "Press to self-destruct" || details.Braille != details.Speech {
		t.Fatalf("details = %#v", details)
	}
}

func TestFormFieldTargetIncludesGenericATSPIButtonRole(t *testing.T) {
	if !MatchTarget("formField")(&model.Node{Role: "button"}) {
		t.Fatal("generic AT-SPI button must be reachable by form-field navigation")
	}
}

func TestGestureNormalization(t *testing.T) {
	if got := NormalizeGesture("Shift+Control+H"); got != "ctrl+shift+h" {
		t.Fatalf("gesture = %q", got)
	}
}

func TestMultiModifierGestureLookupUsesCanonicalOrdering(t *testing.T) {
	for _, test := range []struct {
		gesture string
		layout  string
		want    string
	}{
		{gesture: "ctrl+insert+f", layout: "desktop", want: "find"},
		{gesture: "shift+insert+f3", layout: "desktop", want: "findPrevious"},
		{gesture: "ctrl+capslock+f", layout: "laptop", want: "find"},
	} {
		command, ok := CommandByGesture(test.gesture, test.layout)
		if !ok || command.ID != test.want {
			t.Fatalf("CommandByGesture(%q, %q) = %#v, %v", test.gesture, test.layout, command, ok)
		}
	}
}
