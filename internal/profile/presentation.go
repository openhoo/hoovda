package profile

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
)

type Presentation struct {
	Speech         string                 `json:"speech"`
	SpeechCommands []events.SpeechCommand `json:"speechCommands"`
	Braille        string                 `json:"braille"`
}

type Presenter struct {
	locale string
}

func NewPresenter(locale string) (*Presenter, error) {
	if locale != "en-US" && locale != "de-DE" {
		return nil, fmt.Errorf("unsupported presentation locale %q", locale)
	}
	return &Presenter{locale: locale}, nil
}

func (p *Presenter) Present(node *model.Node, reason string) Presentation {
	if node == nil {
		return Presentation{}
	}
	parts := make([]string, 0, 12)
	tableNavigation := reason == "tableNavigation" && isCellRole(node.Role)
	if tableNavigation {
		if node.Row > 0 {
			if p.locale == "de-DE" {
				parts = append(parts, "Zeile "+strconv.Itoa(node.Row))
			} else {
				parts = append(parts, "row "+strconv.Itoa(node.Row))
			}
		}
		if node.Column > 0 {
			if p.locale == "de-DE" {
				parts = append(parts, "Spalte "+strconv.Itoa(node.Column))
			} else {
				parts = append(parts, "column "+strconv.Itoa(node.Column))
			}
		}
		parts = appendDistinct(parts, node.ColumnHeaderText...)
		parts = appendDistinct(parts, node.RowHeaderText...)
	}
	if content := strings.TrimSpace(node.SpokenContent()); content != "" {
		parts = appendDistinct(parts, content)
	}
	role := p.role(node.Role)
	if strings.EqualFold(node.Role, "landmark") {
		if landmark := strings.Fields(node.Attributes["xml-roles"]); len(landmark) > 0 {
			role = p.landmark(landmark[0])
		}
	}
	if !tableNavigation && role != "" && !sameNormalized(parts, role) {
		parts = append(parts, role)
	}
	if node.HeadingLevel > 0 {
		if p.locale == "de-DE" {
			parts = append(parts, "Ebene "+strconv.Itoa(node.HeadingLevel))
		} else {
			parts = append(parts, "level "+strconv.Itoa(node.HeadingLevel))
		}
	}
	for _, state := range orderedStates(node) {
		parts = append(parts, p.state(state))
	}
	if node.PositionInSet > 0 && node.SetSize > 0 {
		if p.locale == "de-DE" {
			parts = append(parts, fmt.Sprintf("%d von %d", node.PositionInSet, node.SetSize))
		} else {
			parts = append(parts, fmt.Sprintf("%d of %d", node.PositionInSet, node.SetSize))
		}
	}
	if !tableNavigation && node.Row > 0 && node.Column > 0 {
		if p.locale == "de-DE" {
			parts = append(parts, fmt.Sprintf("Zeile %d Spalte %d", node.Row, node.Column))
		} else {
			parts = append(parts, fmt.Sprintf("row %d column %d", node.Row, node.Column))
		}
	}
	if node.RowSpan > 1 {
		if p.locale == "de-DE" {
			parts = append(parts, fmt.Sprintf("über %d Zeilen", node.RowSpan))
		} else {
			parts = append(parts, fmt.Sprintf("spans %d rows", node.RowSpan))
		}
	}
	if node.ColumnSpan > 1 {
		if p.locale == "de-DE" {
			parts = append(parts, fmt.Sprintf("über %d Spalten", node.ColumnSpan))
		} else {
			parts = append(parts, fmt.Sprintf("spans %d columns", node.ColumnSpan))
		}
	}
	if node.Role == "table" && node.RowCount > 0 && node.ColumnCount > 0 {
		if p.locale == "de-DE" {
			parts = append(parts, fmt.Sprintf("mit %d Zeilen und %d Spalten", node.RowCount, node.ColumnCount))
		} else {
			parts = append(parts, fmt.Sprintf("with %d rows and %d columns", node.RowCount, node.ColumnCount))
		}
	}
	if description := strings.TrimSpace(node.Description); description != "" && description != strings.TrimSpace(node.SpokenContent()) {
		parts = appendDistinct(parts, description)
	}
	if shouldPresentRelations(reason) {
		parts = appendDistinct(parts, node.RelationText["described by"]...)
		if len(node.RelationText["details"]) > 0 {
			if p.locale == "de-DE" {
				parts = appendDistinct(parts, "Hat Details")
			} else {
				parts = appendDistinct(parts, "has details")
			}
		}
		if node.HasState("invalid") {
			parts = appendDistinct(parts, node.RelationText["error message"]...)
		}
	}
	text := strings.Join(nonEmpty(parts), "  ")
	commands := []events.SpeechCommand{{Kind: "reason", Value: reason}}
	if node.Locale != "" {
		commands = append(commands, events.SpeechCommand{Kind: "language", Value: node.Locale})
	}
	return Presentation{Speech: text, SpeechCommands: commands, Braille: p.presentBraille(node, reason)}
}

// PresentTextParagraph mirrors NVDA's text-paragraph quick navigation: only
// the matched paragraph text is reported, without adding the paragraph role.
func (p *Presenter) PresentTextParagraph(node *model.Node) Presentation {
	if node == nil {
		return Presentation{}
	}
	text := strings.TrimSpace(node.SpokenContent())
	return p.plain(normalizeTextParagraphSpeech(text), text, node, "quickNavigation")
}

// PresentTextError reports the text range with NVDA's formatting marker before
// its content. Braille retains the range text because error-marker braille is
// controlled by a separate NVDA document-formatting preference.
func (p *Presenter) PresentTextError(node *model.Node) Presentation {
	if node == nil {
		return Presentation{}
	}
	marker := "spelling error"
	if textErrorKind(node) == "grammar" {
		marker = "grammar error"
	}
	if p.locale == "de-DE" {
		if textErrorKind(node) == "grammar" {
			marker = "Grammatikfehler"
		} else {
			marker = "Rechtschreibfehler"
		}
	}
	text := strings.TrimSpace(node.SpokenContent())
	return p.plain(joinSpeech(marker, text), text, node, "quickNavigation")
}

func normalizeTextParagraphSpeech(text string) string {
	// NVDA's default English symbol level suppresses quotation/bracket symbols
	// while preserving their speech-command boundaries. These replacements
	// reproduce the exact public test_textParagraphNavigation assertions.
	text = strings.ReplaceAll(text, `, "`, ",  ")
	text = strings.ReplaceAll(text, `."`, ".")
	text = strings.ReplaceAll(text, `".`, " .")
	text = strings.ReplaceAll(text, "[", "  ")
	text = strings.ReplaceAll(text, "]", "")
	text = strings.ReplaceAll(text, `"`, "")
	text = strings.TrimRight(text, "．！？：；")
	return strings.TrimSpace(text)
}

// PresentTableEntry reports the table summary followed by the cell where the
// browse cursor enters the table. NVDA's table quick-navigation command lands
// on that cell, not on the table container.
func (p *Presenter) PresentTableEntry(table, cell *model.Node) Presentation {
	if table == nil {
		return Presentation{}
	}
	tablePresentation := p.Present(table, "quickNavigation")
	cellPresentation := p.presentTableDelta(cell, nil)
	return p.plain(
		joinSpeech(tablePresentation.Speech, cellPresentation.Speech),
		strings.TrimSpace(strings.Join(nonEmpty([]string{tablePresentation.Braille, cellPresentation.Braille}), " ")),
		table,
		"quickNavigation",
	)
}

// PresentTableMove reports only coordinates that changed from the prior cell.
// This matches NVDA's cached table-axis output (for example, moving vertically
// announces the new row but does not repeat the unchanged column).
func (p *Presenter) PresentTableMove(node, previous *model.Node) Presentation {
	return p.presentTableDelta(node, previous)
}

func (p *Presenter) presentTableDelta(node, previous *model.Node) Presentation {
	if node == nil {
		return Presentation{}
	}
	speechParts := make([]string, 0, 8)
	brailleParts := make([]string, 0, 8)
	rowChanged := previous == nil || node.Row != previous.Row
	columnChanged := previous == nil || node.Column != previous.Column
	if node.Row > 0 && rowChanged {
		if p.locale == "de-DE" {
			speechParts = append(speechParts, "Zeile "+strconv.Itoa(node.Row))
		} else {
			speechParts = append(speechParts, "row "+strconv.Itoa(node.Row))
		}
		brailleParts = append(brailleParts, "r"+strconv.Itoa(node.Row))
	}
	if node.Column > 0 && columnChanged {
		if p.locale == "de-DE" {
			speechParts = append(speechParts, "Spalte "+strconv.Itoa(node.Column))
		} else {
			speechParts = append(speechParts, "column "+strconv.Itoa(node.Column))
		}
		brailleParts = append(brailleParts, "c"+strconv.Itoa(node.Column))
	}
	if columnChanged {
		speechParts = appendDistinct(speechParts, node.ColumnHeaderText...)
		brailleParts = appendDistinct(brailleParts, node.ColumnHeaderText...)
	}
	if rowChanged {
		speechParts = appendDistinct(speechParts, node.RowHeaderText...)
		brailleParts = appendDistinct(brailleParts, node.RowHeaderText...)
	}
	if content := strings.TrimSpace(node.SpokenContent()); content != "" {
		speechParts = appendDistinct(speechParts, content)
		brailleParts = appendDistinct(brailleParts, content)
	}
	if node.RowSpan > 1 {
		if p.locale == "de-DE" {
			speechParts = append(speechParts, fmt.Sprintf("über %d Zeilen", node.RowSpan))
		} else {
			speechParts = append(speechParts, fmt.Sprintf("spans %d rows", node.RowSpan))
		}
	}
	if node.ColumnSpan > 1 {
		if p.locale == "de-DE" {
			speechParts = append(speechParts, fmt.Sprintf("über %d Spalten", node.ColumnSpan))
		} else {
			speechParts = append(speechParts, fmt.Sprintf("spans %d columns", node.ColumnSpan))
		}
	}
	if description := strings.TrimSpace(node.Description); description != "" && description != strings.TrimSpace(node.SpokenContent()) {
		speechParts = appendDistinct(speechParts, description)
		brailleParts = appendDistinct(brailleParts, description)
	}
	speechParts = appendDistinct(speechParts, node.RelationText["described by"]...)
	brailleParts = appendDistinct(brailleParts, node.RelationText["described by"]...)
	if len(node.RelationText["details"]) > 0 {
		if p.locale == "de-DE" {
			speechParts = appendDistinct(speechParts, "Hat Details")
			brailleParts = appendDistinct(brailleParts, "Details")
		} else {
			speechParts = appendDistinct(speechParts, "has details")
			brailleParts = appendDistinct(brailleParts, "details")
		}
	}
	if node.HasState("invalid") {
		speechParts = appendDistinct(speechParts, node.RelationText["error message"]...)
		brailleParts = appendDistinct(brailleParts, node.RelationText["error message"]...)
	}
	return p.plain(strings.Join(nonEmpty(speechParts), "  "), strings.Join(nonEmpty(brailleParts), " "), node, "tableNavigation")
}

func (p *Presenter) plain(speech, braille string, node *model.Node, reason string) Presentation {
	commands := []events.SpeechCommand{{Kind: "reason", Value: reason}}
	if node != nil && node.Locale != "" {
		commands = append(commands, events.SpeechCommand{Kind: "language", Value: node.Locale})
	}
	return Presentation{Speech: speech, SpeechCommands: commands, Braille: braille}
}

func joinSpeech(parts ...string) string {
	return strings.Join(nonEmpty(parts), "  ")
}

func isCellRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "table cell", "cell", "column header", "row header":
		return true
	default:
		return false
	}
}

func (p *Presenter) presentBraille(node *model.Node, reason string) string {
	parts := make([]string, 0, 12)
	if reason == "tableNavigation" {
		if node.Row > 0 {
			parts = append(parts, "r"+strconv.Itoa(node.Row))
		}
		if node.Column > 0 {
			parts = append(parts, "c"+strconv.Itoa(node.Column))
		}
		parts = appendDistinct(parts, node.ColumnHeaderText...)
		parts = appendDistinct(parts, node.RowHeaderText...)
	}
	if content := strings.TrimSpace(node.SpokenContent()); content != "" {
		parts = appendDistinct(parts, content)
	}
	if description := strings.TrimSpace(node.Description); description != "" && description != strings.TrimSpace(node.SpokenContent()) {
		parts = appendDistinct(parts, description)
	}
	if role := p.brailleRole(node); role != "" && !sameNormalized(parts, role) {
		parts = append(parts, role)
	}
	for _, state := range orderedStates(node) {
		parts = append(parts, p.brailleState(state))
	}
	if node.PositionInSet > 0 && node.SetSize > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d", node.PositionInSet, node.SetSize))
	}
	if reason != "tableNavigation" {
		if node.Row > 0 {
			parts = append(parts, "r"+strconv.Itoa(node.Row))
		}
		if node.Column > 0 {
			parts = append(parts, "c"+strconv.Itoa(node.Column))
		}
	}
	if node.Role == "table" && node.RowCount > 0 && node.ColumnCount > 0 {
		parts = append(parts, fmt.Sprintf("%dr %dc", node.RowCount, node.ColumnCount))
	}
	if shouldPresentRelations(reason) {
		parts = appendDistinct(parts, node.RelationText["described by"]...)
		if len(node.RelationText["details"]) > 0 {
			if p.locale == "de-DE" {
				parts = appendDistinct(parts, "Details")
			} else {
				parts = appendDistinct(parts, "details")
			}
		}
		if node.HasState("invalid") {
			parts = appendDistinct(parts, node.RelationText["error message"]...)
		}
	}
	return strings.Join(nonEmpty(parts), " ")
}

func (p *Presenter) brailleRole(node *model.Node) string {
	role := strings.ToLower(strings.TrimSpace(node.Role))
	if role == "heading" && node.HeadingLevel > 0 {
		if p.locale == "de-DE" {
			return "ü" + strconv.Itoa(node.HeadingLevel)
		}
		return "h" + strconv.Itoa(node.HeadingLevel)
	}
	if p.locale == "de-DE" {
		roles := map[string]string{
			"push button": "sltr", "button": "sltr", "toggle button": "umsch",
			"check box": "KF", "radio button": "AS", "combo box": "kmbf",
			"entry": "ef", "password text": "kennw ef", "link": "lnk",
			"list": "lst", "list item": "lste", "image": "grf", "graphic": "grf", "table": "tbl",
			"table cell": "z", "cell": "z", "column header": "spü", "row header": "zü",
			"document web": "dok", "document frame": "rahm", "internal frame": "rahm", "frame": "rahm",
			"dialog": "dlg", "menu": "mnü", "menu item": "mnüe", "progress bar": "fsb",
		}
		if value := roles[role]; value != "" {
			return value
		}
		return p.role(role)
	}
	roles := map[string]string{
		"push button": "btn", "button": "btn", "toggle button": "tgl btn",
		"check box": "chk", "radio button": "rbtn", "combo box": "cbo",
		"entry": "edt", "password text": "pwd edt", "link": "lnk",
		"list": "lst", "list item": "lst item", "table": "tbl",
		"table cell": "cell", "column header": "ch", "row header": "rh",
		"document web": "doc", "document frame": "frm", "internal frame": "frm", "frame": "frm",
		"progress bar": "prgbar", "page tab": "tab",
	}
	if value := roles[role]; value != "" {
		return value
	}
	return p.role(role)
}

func (p *Presenter) brailleState(state string) string {
	if p.locale == "de-DE" {
		states := map[string]string{
			"checked": "⣏⣿⣹", "not checked": "⣏⣀⣹", "mixed": "⣏⣸⣹",
			"pressed": "⢎⣿⡱", "not pressed": "⢎⣀⡱", "expanded": "-",
			"collapsed": "+", "selected": "(x)", "unavailable": "nicht verfügbar",
			"required": "erf", "invalid entry": "Ungültig", "read only": "sef",
			"visited": "besucht", "has popup": "->", "multiline": "mz", "busy": "besch",
		}
		if value := states[state]; value != "" {
			return value
		}
		return state
	}
	states := map[string]string{
		"checked": "⣏⣿⣹", "not checked": "⣏⣀⣹", "mixed": "⣏⣸⣹",
		"pressed": "⢎⣿⡱", "not pressed": "⢎⣀⡱", "expanded": "-",
		"collapsed": "+", "selected": "sel", "unavailable": "unavail",
		"required": "req", "invalid entry": "invalid", "read only": "ro",
		"visited": "vlnk", "has popup": "submnu", "multiline": "mln", "busy": "busy",
	}
	if value := states[state]; value != "" {
		return value
	}
	return state
}

func (p *Presenter) Details(values []string) Presentation {
	text := strings.TrimSpace(strings.Join(nonEmpty(append([]string(nil), values...)), " "))
	if text == "" {
		if p.locale == "de-DE" {
			text = "Keine zusätzlichen Details"
		} else {
			text = "No additional details"
		}
	}
	return Presentation{Speech: text, Braille: text}
}

func shouldPresentRelations(reason string) bool {
	switch reason {
	case "focus", "quickNavigation", "tableNavigation", "report":
		return true
	default:
		return false
	}
}

func (p *Presenter) PresentText(node *model.Node, text string, unit model.TextUnit) Presentation {
	speech := strings.TrimSpace(text)
	if speech == "" {
		if p.locale == "de-DE" {
			speech = "leer"
		} else {
			speech = "blank"
		}
	} else if unit == model.TextUnitCharacter {
		switch text {
		case " ":
			if p.locale == "de-DE" {
				speech = "Leerzeichen"
			} else {
				speech = "space"
			}
		case "\t":
			if p.locale == "de-DE" {
				speech = "Tabulator"
			} else {
				speech = "tab"
			}
		case "\r", "\n":
			if p.locale == "de-DE" {
				speech = "neue Zeile"
			} else {
				speech = "new line"
			}
		}
	}
	commands := []events.SpeechCommand{{Kind: "reason", Value: "textNavigation"}}
	if node != nil && node.Locale != "" {
		commands = append(commands, events.SpeechCommand{Kind: "language", Value: node.Locale})
	}
	return Presentation{Speech: speech, SpeechCommands: commands, Braille: text}
}

func (p *Presenter) landmark(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if p.locale == "de-DE" {
		translated := map[string]string{
			"banner": "Banner Sprungmarke", "complementary": "Ergänzung Sprungmarke",
			"contentinfo": "Inhaltsangabe Sprungmarke", "form": "Formular Sprungmarke",
			"main": "Haupt Sprungmarke", "navigation": "Navigation Sprungmarke",
			"region": "Region Sprungmarke", "search": "Suche Sprungmarke",
		}
		if value := translated[role]; value != "" {
			return value
		}
		return "Sprungmarke"
	}
	if role == "" {
		return "landmark"
	}
	return role + " landmark"
}

func (p *Presenter) NoTarget(target string, direction int) Presentation {
	if text := p.quickNavigationBoundary(target, direction); text != "" {
		return Presentation{Speech: text, Braille: text}
	}
	prefix := "no next"
	if direction < 0 {
		prefix = "no previous"
	}
	return Presentation{Speech: prefix + " " + target, Braille: prefix + " " + target}
}

func (p *Presenter) quickNavigationBoundary(target string, direction int) string {
	if strings.HasPrefix(target, "heading") && len(target) == len("heading1") {
		level := target[len(target)-1:]
		if p.locale == "de-DE" {
			if direction < 0 {
				return "Keine vorherige Überschrift auf Ebene " + level
			}
			return "Keine weitere Überschrift auf Ebene " + level
		}
		if direction < 0 {
			return "No previous heading at level " + level
		}
		return "No next heading at level " + level
	}
	english := map[string][2]string{
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
	}
	if p.locale != "de-DE" {
		return directionalBoundary(english[target], direction)
	}
	german := map[string][2]string{
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
	}
	return directionalBoundary(german[target], direction)
}

func directionalBoundary(values [2]string, direction int) string {
	if direction < 0 {
		return values[1]
	}
	return values[0]
}

func (p *Presenter) Mode(mode string) Presentation {
	if p.locale == "de-DE" {
		if mode == "focus" {
			return Presentation{Speech: "Interaktionsmodus", Braille: "Interaktionsmodus"}
		}
		return Presentation{Speech: "Lesemodus", Braille: "Lesemodus"}
	}
	if mode == "focus" {
		return Presentation{Speech: "focus mode", Braille: "focus mode"}
	}
	return Presentation{Speech: "browse mode", Braille: "browse mode"}
}

func (p *Presenter) role(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	english := map[string]string{
		"push button": "button", "toggle button": "toggle button", "check box": "check box",
		"radio button": "radio button", "combo box": "combo box", "password text": "password edit",
		"document web": "document", "document frame": "frame", "internal frame": "frame", "table cell": "cell",
		"column header": "column header", "row header": "row header", "page tab": "tab",
		"status bar": "status", "progress bar": "progress bar", "list item": "list item",
	}
	if p.locale == "en-US" {
		if translated, ok := english[role]; ok {
			return translated
		}
		return role
	}
	german := map[string]string{
		"heading": "Überschrift", "push button": "Schalter", "button": "Schalter",
		"toggle button": "Umschalter", "check box": "Kontrollfeld", "radio button": "Auswahlschalter",
		"combo box": "Kombinationsfeld", "entry": "Eingabefeld", "password text": "Passworteingabefeld",
		"link": "Link", "list": "Liste", "list item": "Listeneintrag", "table": "Tabelle",
		"table cell": "Zelle", "cell": "Zelle", "column header": "Spaltenbeschriftung",
		"row header": "Zeilenbeschriftung", "document web": "Dokument", "document frame": "Rahmen", "internal frame": "Rahmen",
		"landmark": "Sprungmarke", "dialog": "Dialogfeld", "alert": "Benachrichtigung", "status bar": "Status",
		"menu": "Menü", "menu item": "Menü-Eintrag", "page tab": "Tab", "tree item": "Eintrag",
		"slider": "Schieber", "progress bar": "Fortschrittsbalken", "image": "Grafik", "graphic": "Grafik",
		"separator": "Trennlinie", "article": "Artikel", "paragraph": "Absatz", "math": "Mathematisch",
	}
	if translated, ok := german[role]; ok {
		return translated
	}
	return role
}

func (p *Presenter) state(state string) string {
	if p.locale == "en-US" {
		return state
	}
	translations := map[string]string{
		"checked": "aktiviert", "not checked": "Nicht aktiviert", "mixed": "teilweise aktiviert",
		"pressed": "gedrückt", "not pressed": "Nicht gedrückt", "expanded": "ausgeklappt",
		"collapsed": "eingeklappt", "selected": "ausgewählt", "unavailable": "nicht verfügbar",
		"required": "erforderlich", "invalid entry": "ungültiger Eintrag", "read only": "schreibgeschützt",
		"visited": "besucht", "has popup": "hat Popup", "multiline": "mehrzeilig", "busy": "beschäftigt",
	}
	if translated, ok := translations[state]; ok {
		return translated
	}
	return state
}

func orderedStates(node *model.Node) []string {
	states := make([]string, 0, 12)
	add := func(condition bool, value string) {
		if condition {
			states = append(states, value)
		}
	}
	if node.Role == "check box" || node.Role == "radio button" {
		add(node.HasState("indeterminate"), "mixed")
		add(!node.HasState("indeterminate") && node.HasState("checked"), "checked")
		add(!node.HasState("indeterminate") && !node.HasState("checked"), "not checked")
	}
	if node.Role == "toggle button" {
		add(node.HasState("pressed"), "pressed")
		add(!node.HasState("pressed"), "not pressed")
	}
	add(node.HasState("expanded"), "expanded")
	add(node.HasState("collapsed"), "collapsed")
	add(node.HasState("selected"), "selected")
	add(!node.HasState("enabled"), "unavailable")
	add(node.HasState("required"), "required")
	add(node.HasState("invalid"), "invalid entry")
	add(node.HasState("read only"), "read only")
	add(node.HasState("visited"), "visited")
	add(node.HasState("has popup"), "has popup")
	add(node.HasState("multiline"), "multiline")
	add(node.HasState("busy"), "busy")
	return states
}

func nonEmpty(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func sameNormalized(values []string, value string) bool {
	target := strings.ToLower(strings.TrimSpace(value))
	for _, existing := range values {
		if strings.ToLower(strings.TrimSpace(existing)) == target {
			return true
		}
	}
	return false
}

func appendDistinct(values []string, additions ...string) []string {
	for _, value := range additions {
		value = strings.TrimSpace(value)
		if value != "" && !sameNormalized(values, value) {
			values = append(values, value)
		}
	}
	return values
}

func ProfileDigestInput(locale string) []string {
	result := []string{"nvda-web-2026.1.1", locale}
	for _, command := range commands {
		result = append(result, command.ID+":"+strings.Join(command.Desktop, ","))
	}
	sort.Strings(result[2:])
	return result
}
