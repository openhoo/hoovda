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
	if got.Speech != "Checkout heading level 1" {
		t.Fatalf("speech = %q", got.Speech)
	}
}

func TestGermanCheckboxPresentation(t *testing.T) {
	presenter, _ := NewPresenter("de-DE")
	got := presenter.Present(&model.Node{Role: "check box", Name: "AGB", States: map[string]bool{"enabled": true, "checked": true}}, "focus")
	if !strings.Contains(got.Speech, "Kontrollkästchen aktiviert") {
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
	if got.Speech != "Rates table 3 rows 2 columns" {
		t.Fatalf("speech = %q", got.Speech)
	}
}

func TestGestureNormalization(t *testing.T) {
	if got := NormalizeGesture("Shift+Control+H"); got != "ctrl+shift+h" {
		t.Fatalf("gesture = %q", got)
	}
}
