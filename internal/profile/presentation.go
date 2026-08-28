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
				parts = appendDistinct(parts, "enthält Details")
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
			parts = appendDistinct(parts, "details")
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
		return "h" + strconv.Itoa(node.HeadingLevel)
	}
	if p.locale == "de-DE" {
		roles := map[string]string{
			"push button": "schlt", "button": "schlt", "toggle button": "umsch",
			"check box": "ktrl", "radio button": "opf", "combo box": "kob",
			"entry": "eing", "password text": "kennw eing", "link": "lnk",
			"list": "lst", "list item": "lste", "table": "tbl",
			"table cell": "z", "column header": "spü", "row header": "zü",
			"document web": "dok", "document frame": "rahm", "frame": "rahm",
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
		"document web": "doc", "document frame": "frm", "frame": "frm",
		"progress bar": "prgbar", "page tab": "tab",
	}
	if value := roles[role]; value != "" {
		return value
	}
	return p.role(role)
}

func (p *Presenter) brailleState(state string) string {
	if p.locale == "de-DE" {
		return p.state(state)
	}
	states := map[string]string{
		"checked": "chk", "not checked": "not chk", "mixed": "half chk",
		"pressed": "pressed", "not pressed": "not pressed", "expanded": "expanded",
		"collapsed": "collapsed", "selected": "sel", "unavailable": "unavail",
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
			"banner": "Banner Orientierungspunkt", "complementary": "Ergänzend Orientierungspunkt",
			"contentinfo": "Inhaltsinformation Orientierungspunkt", "form": "Formular Orientierungspunkt",
			"main": "Hauptbereich Orientierungspunkt", "navigation": "Navigation Orientierungspunkt",
			"region": "Region Orientierungspunkt", "search": "Suche Orientierungspunkt",
		}
		if value := translated[role]; value != "" {
			return value
		}
		return "Orientierungspunkt"
	}
	if role == "" {
		return "landmark"
	}
	return role + " landmark"
}

func (p *Presenter) NoTarget(target string, direction int) Presentation {
	word := target
	if p.locale == "de-DE" {
		prefix := "Kein nächstes"
		if direction < 0 {
			prefix = "Kein vorheriges"
		}
		return Presentation{Speech: prefix + " " + word, Braille: prefix + " " + word}
	}
	prefix := "no next"
	if direction < 0 {
		prefix = "no previous"
	}
	return Presentation{Speech: prefix + " " + word, Braille: prefix + " " + word}
}

func (p *Presenter) Mode(mode string) Presentation {
	if p.locale == "de-DE" {
		if mode == "focus" {
			return Presentation{Speech: "Fokusmodus", Braille: "Fokusmodus"}
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
		"document web": "document", "document frame": "frame", "table cell": "cell",
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
		"toggle button": "Umschalter", "check box": "Kontrollkästchen", "radio button": "Optionsfeld",
		"combo box": "Kombinationsfeld", "entry": "Eingabefeld", "password text": "Passworteingabefeld",
		"link": "Link", "list": "Liste", "list item": "Listeneintrag", "table": "Tabelle",
		"table cell": "Zelle", "cell": "Zelle", "column header": "Spaltenüberschrift",
		"row header": "Zeilenüberschrift", "document web": "Dokument", "document frame": "Rahmen",
		"landmark": "Orientierungspunkt", "dialog": "Dialog", "alert": "Warnung", "status bar": "Status",
		"menu": "Menü", "menu item": "Menüeintrag", "page tab": "Registerkarte", "tree item": "Baumeintrag",
		"slider": "Schieberegler", "progress bar": "Fortschrittsanzeige", "image": "Grafik",
		"separator": "Trennlinie", "article": "Artikel", "paragraph": "Absatz", "math": "Mathematik",
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
		"checked": "aktiviert", "not checked": "nicht aktiviert", "mixed": "teilweise aktiviert",
		"pressed": "gedrückt", "not pressed": "nicht gedrückt", "expanded": "erweitert",
		"collapsed": "reduziert", "selected": "ausgewählt", "unavailable": "nicht verfügbar",
		"required": "erforderlich", "invalid entry": "ungültige Eingabe", "read only": "schreibgeschützt",
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
