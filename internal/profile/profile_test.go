package profile

import (
	"fmt"
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
	if got.Speech != "AGB  Kontrollfeld  aktiviert" || got.Braille != "AGB KF ⣏⣿⣹" {
		t.Fatalf("presentation = %#v", got)
	}
}

func TestGermanPresentationMatchesPinnedNVDAStrings(t *testing.T) {
	presenter, _ := NewPresenter("de-DE")
	details := presenter.Present(&model.Node{
		Role: "push button", Name: "push me", States: map[string]bool{"enabled": true},
		RelationText: map[string][]string{"details": {"Press to self-destruct"}},
	}, "focus")
	if details.Speech != "push me  Schalter  Hat Details" || details.Braille != "push me sltr Details" {
		t.Fatalf("details presentation = %#v", details)
	}
	if got := presenter.Mode("focus"); got.Speech != "Interaktionsmodus" || got.Braille != "Interaktionsmodus" {
		t.Fatalf("focus mode = %#v", got)
	}
	if got := presenter.Mode("browse"); got.Speech != "Lesemodus" || got.Braille != "Lesemodus" {
		t.Fatalf("browse mode = %#v", got)
	}
}

func TestSelectedStateIsPresentedOnce(t *testing.T) {
	presenter, _ := NewPresenter("en-US")
	got := presenter.Present(&model.Node{Role: "list item", Name: "One", States: map[string]bool{"enabled": true, "selected": true}}, "focus")
	if got.Speech != "One  list item  selected" || got.Braille != "One lst item sel" {
		t.Fatalf("presentation = %#v", got)
	}
}

func TestGermanHeadingAndLandmarkBrailleUsePinnedNVDALabels(t *testing.T) {
	presenter, _ := NewPresenter("de-DE")
	heading := presenter.Present(&model.Node{Role: "heading", Name: "Kasse", HeadingLevel: 2, States: map[string]bool{"enabled": true}}, "quickNavigation")
	if heading.Speech != "Kasse  Überschrift  Ebene 2" || heading.Braille != "Kasse ü2" {
		t.Fatalf("heading = %#v", heading)
	}
	landmark := presenter.Present(&model.Node{Role: "landmark", Attributes: map[string]string{"xml-roles": "main"}, States: map[string]bool{"enabled": true}}, "quickNavigation")
	if landmark.Speech != "Haupt Sprungmarke" {
		t.Fatalf("landmark = %#v", landmark)
	}
}

func TestQuickNavigationBoundariesMatchPinnedNVDACatalog(t *testing.T) {
	want := map[string]map[string][2]string{
		"en-US": {
			"heading":        {"no next heading", "no previous heading"},
			"table":          {"no next table", "no previous table"},
			"link":           {"no next link", "no previous link"},
			"visitedLink":    {"no next visited link", "no previous visited link"},
			"unvisitedLink":  {"no next unvisited link", "no previous unvisited link"},
			"formField":      {"no next form field", "no previous form field"},
			"list":           {"no next list", "no previous list"},
			"listItem":       {"no next list item", "no previous list item"},
			"button":         {"no next button", "no previous button"},
			"edit":           {"no next edit field", "no previous edit field"},
			"frame":          {"no next frame", "no previous frame"},
			"separator":      {"no next separator", "no previous separator"},
			"radioButton":    {"no next radio button", "no previous radio button"},
			"comboBox":       {"no next combo box", "no previous combo box"},
			"checkBox":       {"no next check box", "no previous check box"},
			"graphic":        {"no next graphic", "no previous graphic"},
			"blockQuote":     {"no next block quote", "no previous block quote"},
			"notLinkBlock":   {"no more text after a block of links", "no more text before a block of links"},
			"landmark":       {"no next landmark", "no previous landmark"},
			"embeddedObject": {"no next embedded object", "no previous embedded object"},
			"annotation":     {"no next annotation", "no previous annotation"},
			"error":          {"no next error", "no previous error"},
			"textParagraph":  {"no next text paragraph", "no previous text paragraph"},
		},
		"de-DE": {
			"heading":        {"Keine weitere Überschrift", "Keine vorherige Überschrift"},
			"table":          {"Keine weitere Tabelle", "Keine vorherige Tabelle"},
			"link":           {"Kein weiterer Link", "Kein vorheriger Link"},
			"visitedLink":    {"Kein weiterer besuchter Link", "Kein vorheriger besuchter Link"},
			"unvisitedLink":  {"Kein weiterer unbesuchter Link", "Kein vorheriger unbesuchter Link"},
			"formField":      {"Kein weiteres Formularfeld", "Kein vorheriges Formularfeld"},
			"list":           {"Keine weitere Liste", "Keine vorherige Liste"},
			"listItem":       {"Kein weiterer Listeneintrag", "Kein vorheriger Listeneintrag"},
			"button":         {"Kein weiterer Schalter", "Kein vorheriger Schalter"},
			"edit":           {"Kein weiteres Eingabefeld", "Kein vorheriges Eingabefeld"},
			"frame":          {"Kein weiterer Rahmen", "Kein weiterer Rahmen"},
			"separator":      {"Keine weitere Trennlinie", "Keine vorherige Trennlinie"},
			"radioButton":    {"Kein weiterer Auswahlschalter", "Kein vorheriger Auswahlschalter"},
			"comboBox":       {"Kein weiteres Kombinationsfeld", "Kein vorheriges Kombinationsfeld"},
			"checkBox":       {"Kein weiteres Kontrollfeld", "Kein vorheriges Kontrollfeld"},
			"graphic":        {"Keine weitere Grafik", "Keine vorherige Grafik"},
			"blockQuote":     {"Kein weiterer Zitatblock", "Kein vorheriger Zitatblock"},
			"notLinkBlock":   {"Kein weiterer Text nach dem Abschnitt der Links", "Kein weiterer Text vor dem Abschnitt der Links"},
			"landmark":       {"Keine weitere Sprungmarke", "Keine vorherige Sprungmarke"},
			"embeddedObject": {"Kein weiteres eingebettetes Objekt", "Kein vorheriges eingebettetes Objekt"},
			"annotation":     {"Keine weitere Anmerkung", "Keine vorherige Anmerkung"},
			"error":          {"Kein weiterer Fehler", "Kein vorheriger Fehler"},
			"textParagraph":  {"Keine weiteren Absätze", "kein vorheriger Textabsatz"},
		},
	}
	for locale, targets := range want {
		presenter, _ := NewPresenter(locale)
		for target, messages := range targets {
			for index, direction := range []int{1, -1} {
				got := presenter.NoTarget(target, direction)
				if got.Speech != messages[index] || got.Braille != messages[index] {
					t.Errorf("%s %s direction %d = speech %q, braille %q; want %q", locale, target, direction, got.Speech, got.Braille, messages[index])
				}
			}
		}
		for level := 1; level <= 9; level++ {
			target := fmt.Sprintf("heading%d", level)
			next := fmt.Sprintf("No next heading at level %d", level)
			previous := fmt.Sprintf("No previous heading at level %d", level)
			if locale == "de-DE" {
				next = fmt.Sprintf("Keine weitere Überschrift auf Ebene %d", level)
				previous = fmt.Sprintf("Keine vorherige Überschrift auf Ebene %d", level)
			}
			if got := presenter.NoTarget(target, 1).Speech; got != next {
				t.Errorf("%s %s next = %q; want %q", locale, target, got, next)
			}
			if got := presenter.NoTarget(target, -1).Speech; got != previous {
				t.Errorf("%s %s previous = %q; want %q", locale, target, got, previous)
			}
		}
	}
}

func TestQuickNavigationCommandCatalogHasPinnedBoundaries(t *testing.T) {
	presenter, _ := NewPresenter("en-US")
	for _, command := range Commands() {
		if command.Category != "quickNavigation" {
			continue
		}
		if got := presenter.quickNavigationBoundary(command.Target, command.Direction); got == "" {
			t.Errorf("quick-navigation command %s target %q has no pinned boundary", command.ID, command.Target)
		}
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

func TestTextParagraphPresentationOmitsRole(t *testing.T) {
	presenter, _ := NewPresenter("en-US")
	got := presenter.PresentTextParagraph(&model.Node{Role: "paragraph", Text: "Hello, world!"})
	if got.Speech != "Hello, world!" || got.Braille != "Hello, world!" {
		t.Fatalf("presentation = %#v", got)
	}
}

func TestTextParagraphPresentationMatchesPinnedNVDASymbolOutput(t *testing.T) {
	presenter, _ := NewPresenter("en-US")
	tests := map[string]string{
		`He replied, "That's wonderful."`:    "He replied,  That's wonderful.",
		`He replied, "That's wonderful".`:    "He replied,  That's wonderful .",
		`He replied, "That's wonderful."[4]`: "He replied,  That's wonderful.  4",
		"我不会说中文！":                            "我不会说中文",
	}
	for input, want := range tests {
		got := presenter.PresentTextParagraph(&model.Node{Role: "paragraph", Text: input})
		if got.Speech != want {
			t.Errorf("speech for %q = %q, want %q", input, got.Speech, want)
		}
	}
}

func TestTableEntryAndAxisCaching(t *testing.T) {
	presenter, _ := NewPresenter("en-US")
	table := &model.Node{Role: "table", RowCount: 2, ColumnCount: 2, States: map[string]bool{"enabled": true}}
	first := &model.Node{Role: "column header", Name: "First heading", Row: 1, Column: 1}
	entry := presenter.PresentTableEntry(table, first)
	if entry.Speech != "table  with 2 rows and 2 columns  row 1  column 1  First heading" {
		t.Fatalf("entry = %#v", entry)
	}
	second := &model.Node{
		Role: "table cell", Name: "First content cell", Row: 2, Column: 1,
		ColumnHeaderText: []string{"First heading"},
		RelationText:     map[string][]string{"described by": {"Cell help"}},
	}
	move := presenter.PresentTableMove(second, first)
	if move.Speech != "row 2  First content cell  Cell help" || move.Braille != "r2 First content cell Cell help" {
		t.Fatalf("move = %#v", move)
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

func TestTextParagraphTargetMatchesPinnedNVDADefaultRegex(t *testing.T) {
	for _, text := range []string{
		"Hello, world!",
		`He replied, "That's wonderful."`,
		`He replied, "That's wonderful".`,
		`He replied, "That's wonderful."[4]`,
		"Предложение по-русски.",
		"我不会说中文！",
	} {
		if !MatchTarget("textParagraph")(&model.Node{Role: "paragraph", Text: text}) {
			t.Errorf("expected match for %q", text)
		}
	}
	for _, text := range []string{"Header", "Liberal MP: 1904–1908", ".", "…", "5.", "test....", "a.b"} {
		if MatchTarget("textParagraph")(&model.Node{Role: "paragraph", Text: text}) {
			t.Errorf("unexpected match for %q", text)
		}
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
