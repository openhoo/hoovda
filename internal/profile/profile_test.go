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

func TestCommandNeedsGraphExcludesStateOnlyBrailleTether(t *testing.T) {
	tether, ok := CommandByID("brailleToggleTether")
	if !ok {
		t.Fatal("brailleToggleTether missing")
	}
	if CommandNeedsGraph(tether) {
		t.Fatal("brailleToggleTether must not refresh the accessibility graph")
	}
	pan, ok := CommandByID("braillePanForward")
	if !ok || !CommandNeedsGraph(pan) {
		t.Fatal("braillePanForward must refresh a dirty accessibility graph")
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

func TestReportFormattingMatchesPinnedNVDAStrings(t *testing.T) {
	node := &model.Node{
		Attributes: map[string]string{"text-align": "left"},
		TextAttributeRuns: []model.TextAttributeRun{{Start: 0, End: 24, Attributes: map[string]string{
			"family-name": "Liberation Serif", "size": "18pt", "fg-color": "0,0,0",
			"bg-color": "255,255,255", "weight": "700",
		}}},
	}
	english, _ := NewPresenter("en-US")
	if got := english.TextFormatting(node, 0).Speech; got != "Times New Roman 18 pt black on white bold align left" {
		t.Fatalf("English formatting = %q", got)
	}
	german, _ := NewPresenter("de-DE")
	if got := german.TextFormatting(node, 0).Speech; got != "Times New Roman 18 pt Schwarz auf Weiß fett linksbündig" {
		t.Fatalf("German formatting = %q", got)
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
	if landmark.Speech != "Haupt Sprungmarke" || landmark.Braille != "spmk Haupt" {
		t.Fatalf("landmark = %#v", landmark)
	}
}

func TestQuickNavigationBoundariesMatchPinnedNVDACatalog(t *testing.T) {
	want := map[string]map[string][2]string{
		"en-US": {
			"heading":           {"no next heading", "no previous heading"},
			"table":             {"no next table", "no previous table"},
			"link":              {"no next link", "no previous link"},
			"visitedLink":       {"no next visited link", "no previous visited link"},
			"unvisitedLink":     {"no next unvisited link", "no previous unvisited link"},
			"formField":         {"no next form field", "no previous form field"},
			"list":              {"no next list", "no previous list"},
			"listItem":          {"no next list item", "no previous list item"},
			"button":            {"no next button", "no previous button"},
			"edit":              {"no next edit field", "no previous edit field"},
			"frame":             {"no next frame", "no previous frame"},
			"separator":         {"no next separator", "no previous separator"},
			"radioButton":       {"no next radio button", "no previous radio button"},
			"comboBox":          {"no next combo box", "no previous combo box"},
			"checkBox":          {"no next check box", "no previous check box"},
			"graphic":           {"no next graphic", "no previous graphic"},
			"blockQuote":        {"no next block quote", "no previous block quote"},
			"notLinkBlock":      {"no more text after a block of links", "no more text before a block of links"},
			"landmark":          {"no next landmark", "no previous landmark"},
			"embeddedObject":    {"no next embedded object", "no previous embedded object"},
			"annotation":        {"no next annotation", "no previous annotation"},
			"error":             {"Not supported in this document", "Not supported in this document"},
			"textParagraph":     {"no next text paragraph", "no previous text paragraph"},
			"article":           {"no next article", "no previous article"},
			"figure":            {"no next figure", "no previous figure"},
			"grouping":          {"no next grouping", "no previous grouping"},
			"tab":               {"no next tab", "no previous tab"},
			"menuItem":          {"no next menu item", "no previous menu item"},
			"toggleButton":      {"no next toggle button", "no previous toggle button"},
			"progressBar":       {"no next progress bar", "no previous progress bar"},
			"reference":         {"Not supported in this document", "Not supported in this document"},
			"math":              {"no next math formula", "no previous math formula"},
			"verticalParagraph": {"no next vertically aligned paragraph", "no previous vertically aligned paragraph"},
			"sameStyle":         {"No next same style text", "No previous same style text"},
			"differentStyle":    {"No next different style text", "No previous different style text"},
		},
		"de-DE": {
			"heading":           {"Keine weitere Überschrift", "Keine vorherige Überschrift"},
			"table":             {"Keine weitere Tabelle", "Keine vorherige Tabelle"},
			"link":              {"Kein weiterer Link", "Kein vorheriger Link"},
			"visitedLink":       {"Kein weiterer besuchter Link", "Kein vorheriger besuchter Link"},
			"unvisitedLink":     {"Kein weiterer unbesuchter Link", "Kein vorheriger unbesuchter Link"},
			"formField":         {"Kein weiteres Formularfeld", "Kein vorheriges Formularfeld"},
			"list":              {"Keine weitere Liste", "Keine vorherige Liste"},
			"listItem":          {"Kein weiterer Listeneintrag", "Kein vorheriger Listeneintrag"},
			"button":            {"Kein weiterer Schalter", "Kein vorheriger Schalter"},
			"edit":              {"Kein weiteres Eingabefeld", "Kein vorheriges Eingabefeld"},
			"frame":             {"Kein weiterer Rahmen", "Kein weiterer Rahmen"},
			"separator":         {"Keine weitere Trennlinie", "Keine vorherige Trennlinie"},
			"radioButton":       {"Kein weiterer Auswahlschalter", "Kein vorheriger Auswahlschalter"},
			"comboBox":          {"Kein weiteres Kombinationsfeld", "Kein vorheriges Kombinationsfeld"},
			"checkBox":          {"Kein weiteres Kontrollfeld", "Kein vorheriges Kontrollfeld"},
			"graphic":           {"Keine weitere Grafik", "Keine vorherige Grafik"},
			"blockQuote":        {"Kein weiterer Zitatblock", "Kein vorheriger Zitatblock"},
			"notLinkBlock":      {"Kein weiterer Text nach dem Abschnitt der Links", "Kein weiterer Text vor dem Abschnitt der Links"},
			"landmark":          {"Keine weitere Sprungmarke", "Keine vorherige Sprungmarke"},
			"embeddedObject":    {"Kein weiteres eingebettetes Objekt", "Kein vorheriges eingebettetes Objekt"},
			"annotation":        {"Keine weitere Anmerkung", "Keine vorherige Anmerkung"},
			"error":             {"Keine Unterstützung in diesem Dokument", "Keine Unterstützung in diesem Dokument"},
			"textParagraph":     {"Keine weiteren Absätze", "kein vorheriger Textabsatz"},
			"article":           {"Kein weiterer Artikel", "Kein vorheriger Artikel"},
			"figure":            {"Keine weiteren Abbildungen", "Keine vorherigen Abbildungen"},
			"grouping":          {"Keine weitere Gruppierung", "Keine vorherige Gruppierung"},
			"tab":               {"Keine weitere Registerkarte", "Keine vorherige Registerkarte"},
			"menuItem":          {"Keine weiteren Menü-Elemente", "Keine vorherigen Menü-Elemente"},
			"toggleButton":      {"Kein weiterer Umschalter", "Kein vorheriger Umschalter"},
			"progressBar":       {"Keine weiteren Fortschrittsbalken", "Keine vorherigen Fortschrittsbalken"},
			"reference":         {"Keine Unterstützung in diesem Dokument", "Keine Unterstützung in diesem Dokument"},
			"math":              {"Keine weiteren mathematischen Formeln", "Keine vorherigen mathematischen Formeln"},
			"verticalParagraph": {"Keine weiteren vertikal ausgerichteten Absätze", "Keine vorherigen vertikal ausgerichteten Absätze"},
			"sameStyle":         {"Keine weiteren Texte des gleichen Stils", "Keine vorherigen Texte des gleichen Stils"},
			"differentStyle":    {"Keine weiteren Texte unterschiedlichen Stils", "Keine vorherigen Texte unterschiedlichen Stils"},
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

func TestTextErrorsUseExplicitLeafTextAttributes(t *testing.T) {
	for _, kind := range []string{"spelling", "grammar"} {
		node := &model.Node{
			Role: "static",
			Text: kind,
			TextAttributeRuns: []model.TextAttributeRun{{
				Start:      0,
				End:        len(kind),
				Attributes: map[string]string{"invalid": kind},
			}},
		}
		if !MatchTarget("error")(node) {
			t.Errorf("explicit %s text attribute was not matched", kind)
		}
	}
	parent := &model.Node{
		Role:     "section",
		Children: []model.ObjectID{{Bus: "app", Path: "/text"}},
		Attributes: map[string]string{
			"invalid": "spelling",
		},
	}
	if MatchTarget("error")(parent) {
		t.Fatal("text-error wrapper must not duplicate its leaf range")
	}
	if MatchTarget("error")(&model.Node{Role: "entry", States: map[string]bool{"invalid": true}}) {
		t.Fatal("generic invalid state must not be treated as a text error")
	}
}

func TestTextErrorPresentationUsesPinnedNVDAMarkers(t *testing.T) {
	english, _ := NewPresenter("en-US")
	spelling := &model.Node{Role: "static", Text: "caat", Attributes: map[string]string{"invalid": "spelling"}}
	if got := english.PresentTextError(spelling); got.Speech != "spelling error  caat" || got.Braille != "caat" {
		t.Fatalf("English spelling presentation = %#v", got)
	}
	german, _ := NewPresenter("de-DE")
	grammar := &model.Node{Role: "static", Text: "a dog", Attributes: map[string]string{"invalid": "grammar"}}
	if got := german.PresentTextError(grammar); got.Speech != "Grammatikfehler  a dog" || got.Braille != "a dog" {
		t.Fatalf("German grammar presentation = %#v", got)
	}
}

func TestChromiumInternalFrameMatchesFrameNavigation(t *testing.T) {
	node := &model.Node{Role: "internal frame", Name: "Shipping help", States: map[string]bool{"enabled": true}}
	if !MatchTarget("frame")(node) {
		t.Fatal("Chromium internal frame was not matched")
	}
	english, _ := NewPresenter("en-US")
	if got := english.Present(node, "quickNavigation"); got.Speech != "Shipping help  frame" || got.Braille != "Shipping help frm" {
		t.Fatalf("internal frame presentation = %#v", got)
	}
	if MatchTarget("frame")(&model.Node{Role: "document frame"}) {
		t.Fatal("nested frame document must not duplicate its internal-frame container")
	}
}

func TestEnglishLandmarkPresentationUsesSemanticRole(t *testing.T) {
	presenter, _ := NewPresenter("en-US")
	got := presenter.Present(&model.Node{Role: "landmark", Attributes: map[string]string{"xml-roles": "main"}, States: map[string]bool{"enabled": true}}, "quickNavigation")
	if got.Speech != "main landmark" || got.Braille != "lmk main" {
		t.Fatalf("presentation = %#v", got)
	}
}

func TestContainerEntryPresentationsIncludeFirstReadableItem(t *testing.T) {
	presenter, _ := NewPresenter("en-US")
	first := &model.Node{Role: "heading", Name: "Checkout", HeadingLevel: 1, States: map[string]bool{"enabled": true}}
	landmark := presenter.PresentLandmarkEntry(&model.Node{Role: "landmark", Attributes: map[string]string{"xml-roles": "main"}, States: map[string]bool{"enabled": true}}, first)
	if landmark.Speech != "main landmark  Checkout  heading  level 1" || landmark.Braille != "lmk main Checkout h1" {
		t.Fatalf("landmark = %#v", landmark)
	}
	list := presenter.PresentListEntry(
		&model.Node{Role: "list", SetSize: 2, States: map[string]bool{"enabled": true}},
		&model.Node{Role: "list item", Text: "• First item", States: map[string]bool{"enabled": true}},
		2,
	)
	if list.Speech != "list  with 2 items  • First item" || list.Braille != "lst2 • First item" {
		t.Fatalf("list = %#v", list)
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
	for _, role := range []string{"button", "page tab", "menu item"} {
		if !MatchTarget("formField")(&model.Node{Role: role}) {
			t.Fatalf("AT-SPI %s must be reachable by form-field navigation", role)
		}
	}
}

func TestAnnotationTargetAndPresentationCoverTrackedChanges(t *testing.T) {
	for _, role := range []string{"annotation", "content insertion", "content deletion"} {
		if !MatchTarget("annotation")(&model.Node{Role: role}) {
			t.Fatalf("AT-SPI %s must be reachable by annotation navigation", role)
		}
	}
	if MatchTarget("annotation")(&model.Node{Role: "section", Attributes: map[string]string{"xml-roles": "comment"}}) {
		t.Fatal("ARIA comment is not an NVDA annotation quick-navigation target")
	}
	presenter, _ := NewPresenter("en-US")
	got := presenter.Present(&model.Node{Role: "content insertion", Text: "Added", States: map[string]bool{"enabled": true}}, "quickNavigation")
	if got.Speech != "Added  inserted" || got.Braille != "Added ins" {
		t.Fatalf("insertion presentation = %#v", got)
	}
}

func TestChromiumQuickNavigationExcludesSelectOptionsAndReferences(t *testing.T) {
	if MatchTarget("menuItem")(&model.Node{Role: "menu item", Attributes: map[string]string{"tag": "option"}}) {
		t.Fatal("collapsed select option must not be treated as a browse-mode menu item")
	}
	if !MatchTarget("menuItem")(&model.Node{Role: "menu item", Attributes: map[string]string{"tag": "button"}}) {
		t.Fatal("ARIA menu item must remain reachable")
	}
	if MatchTarget("reference")(&model.Node{Role: "link", Attributes: map[string]string{"xml-roles": "doc-biblioref"}}) {
		t.Fatal("Chromium NVDA profile must report reference navigation as unsupported")
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
	article := &model.Node{Role: "article", Name: "Article label", Text: "Article sentence."}
	if !MatchTarget("textParagraph")(article) {
		t.Fatal("article text must be reachable by text-paragraph navigation")
	}
	presenter, _ := NewPresenter("en-US")
	if got := presenter.PresentTextParagraph(article); got.Speech != "Article sentence." {
		t.Fatalf("article paragraph presentation = %#v", got)
	}
	labelledArticle := &model.Node{Role: "article", Name: "Some name.", Text: "\ufffc\ufffc"}
	if MatchTarget("textParagraph")(labelledArticle) {
		t.Fatal("article accessible name must not be mistaken for body paragraph text")
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
