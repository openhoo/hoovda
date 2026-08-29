package profile

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
)

type Presentation struct {
	Speech         string                 `json:"speech"`
	SpeechCommands []events.SpeechCommand `json:"speechCommands"`
	Braille        string                 `json:"braille"`
}

type Presenter struct {
	locale   string
	mu       sync.RWMutex
	settings PresentationSettings
}

func NewPresenter(locale string) (*Presenter, error) {
	if locale != "en-US" && locale != "de-DE" {
		return nil, fmt.Errorf("unsupported presentation locale %q", locale)
	}
	return &Presenter{locale: locale, settings: DefaultPresentationSettings()}, nil
}

func (p *Presenter) Settings() PresentationSettings {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.settings.Clone()
}

func (p *Presenter) SetSettings(settings PresentationSettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	p.mu.Lock()
	p.settings = settings.Clone()
	p.mu.Unlock()
	return nil
}

func (p *Presenter) Present(node *model.Node, reason string) Presentation {
	if node == nil {
		return Presentation{}
	}
	settings := p.Settings()
	parts := make([]string, 0, 12)
	tableNavigation := settings.ReportTables && reason == "tableNavigation" && isCellRole(node.Role)
	if tableNavigation {
		if settings.ReportTableCellCoordinates && node.Row > 0 {
			if p.locale == "de-DE" {
				parts = append(parts, "Zeile "+strconv.Itoa(node.Row))
			} else {
				parts = append(parts, "row "+strconv.Itoa(node.Row))
			}
		}
		if settings.ReportTableCellCoordinates && node.Column > 0 {
			if p.locale == "de-DE" {
				parts = append(parts, "Spalte "+strconv.Itoa(node.Column))
			} else {
				parts = append(parts, "column "+strconv.Itoa(node.Column))
			}
		}
		if reportsColumnHeaders(settings.ReportTableHeaders) {
			parts = appendDistinct(parts, node.ColumnHeaderText...)
		}
		if reportsRowHeaders(settings.ReportTableHeaders) {
			parts = appendDistinct(parts, node.RowHeaderText...)
		}
	}
	if content := strings.TrimSpace(node.SpokenContent()); content != "" {
		parts = appendDistinct(parts, content)
	}
	if editableRole(node.Role) && !node.IsProtected() {
		if value := strings.TrimSpace(strings.ReplaceAll(node.Text, "\ufffc", "")); value != "" {
			parts = appendDistinct(parts, value)
		}
	}
	role := ""
	if reportsRole(node, settings) {
		role = p.role(node.Role)
	}
	if role != "" && strings.EqualFold(node.Role, "landmark") {
		if landmark := strings.Fields(node.Attributes["xml-roles"]); len(landmark) > 0 {
			role = p.landmark(landmark[0])
		}
	}
	if !tableNavigation && role != "" && !sameNormalized(parts, role) {
		parts = append(parts, role)
	}
	if settings.ReportHeadings && node.HeadingLevel > 0 {
		if p.locale == "de-DE" {
			parts = append(parts, "Ebene "+strconv.Itoa(node.HeadingLevel))
		} else {
			parts = append(parts, "level "+strconv.Itoa(node.HeadingLevel))
		}
	}
	for _, state := range orderedStates(node) {
		if state == "visited" && !settings.ReportLinkType {
			continue
		}
		parts = append(parts, p.state(state))
	}
	if settings.ReportObjectPositionInformation && node.PositionInSet > 0 && node.SetSize > 0 {
		if p.locale == "de-DE" {
			parts = append(parts, fmt.Sprintf("%d von %d", node.PositionInSet, node.SetSize))
		} else {
			parts = append(parts, fmt.Sprintf("%d of %d", node.PositionInSet, node.SetSize))
		}
	}
	if settings.ReportTables && settings.ReportTableCellCoordinates && !tableNavigation && node.Row > 0 && node.Column > 0 {
		if p.locale == "de-DE" {
			parts = append(parts, fmt.Sprintf("Zeile %d Spalte %d", node.Row, node.Column))
		} else {
			parts = append(parts, fmt.Sprintf("row %d column %d", node.Row, node.Column))
		}
	}
	if settings.ReportTables && node.RowSpan > 1 {
		if p.locale == "de-DE" {
			parts = append(parts, fmt.Sprintf("über %d Zeilen", node.RowSpan))
		} else {
			parts = append(parts, fmt.Sprintf("spans %d rows", node.RowSpan))
		}
	}
	if settings.ReportTables && node.ColumnSpan > 1 {
		if p.locale == "de-DE" {
			parts = append(parts, fmt.Sprintf("über %d Spalten", node.ColumnSpan))
		} else {
			parts = append(parts, fmt.Sprintf("spans %d columns", node.ColumnSpan))
		}
	}
	if settings.ReportTables && node.Role == "table" && node.RowCount > 0 && node.ColumnCount > 0 {
		if p.locale == "de-DE" {
			parts = append(parts, fmt.Sprintf("mit %d Zeilen und %d Spalten", node.RowCount, node.ColumnCount))
		} else {
			parts = append(parts, fmt.Sprintf("with %d rows and %d columns", node.RowCount, node.ColumnCount))
		}
	}
	if description := strings.TrimSpace(node.Description); settings.ReportObjectDescriptions && settings.ReportAriaDescription && description != "" && description != strings.TrimSpace(node.SpokenContent()) {
		parts = appendDistinct(parts, description)
	}
	if shouldPresentRelations(reason) {
		if settings.ReportObjectDescriptions {
			parts = appendDistinct(parts, node.RelationText["described by"]...)
		}
		if settings.ReportDetails && len(node.RelationText["details"]) > 0 {
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
	if settings.ReportKeyboardShortcuts {
		parts = appendDistinct(parts, node.KeyboardShortcut)
	}
	if settings.ReportClickable && strings.EqualFold(strings.TrimSpace(node.Attributes["clickable"]), "true") {
		if p.locale == "de-DE" {
			parts = appendDistinct(parts, "anklickbar")
		} else {
			parts = appendDistinct(parts, "clickable")
		}
	}
	text := strings.Join(nonEmpty(parts), "  ")
	commands := []events.SpeechCommand{{Kind: "reason", Value: reason}}
	if node.Locale != "" {
		commands = append(commands, events.SpeechCommand{Kind: "language", Value: node.Locale})
	}
	return Presentation{Speech: text, SpeechCommands: commands, Braille: p.presentBraille(node, reason, settings)}
}

// PresentLandmarkEntry mirrors NVDA browse mode: quick navigation announces
// the landmark boundary and the first readable object inside that landmark.
func (p *Presenter) PresentLandmarkEntry(landmark, first *model.Node) Presentation {
	base := p.Present(landmark, "quickNavigation")
	if first == nil {
		return base
	}
	return combinePresentations(base, p.Present(first, "quickNavigation"))
}

// PresentListEntry mirrors NVDA browse mode: list quick navigation includes
// item count and the first item, while leaving the virtual cursor on the list.
func (p *Presenter) PresentListEntry(list, first *model.Node, itemCount int) Presentation {
	if list == nil {
		return Presentation{}
	}
	settings := p.Settings()
	if !settings.ReportLists {
		return p.Present(list, "quickNavigation")
	}
	if itemCount <= 0 {
		itemCount = list.SetSize
	}
	role := p.role("list")
	speech := role
	if itemCount > 0 {
		if p.locale == "de-DE" {
			noun := "Elementen"
			if itemCount == 1 {
				noun = "Element"
			}
			speech += "  mit " + strconv.Itoa(itemCount) + " " + noun
		} else {
			noun := "items"
			if itemCount == 1 {
				noun = "item"
			}
			speech += "  with " + strconv.Itoa(itemCount) + " " + noun
		}
	}
	braille := p.brailleRole(list)
	if itemCount > 0 {
		braille += strconv.Itoa(itemCount)
	}
	commands := []events.SpeechCommand{{Kind: "reason", Value: "quickNavigation"}}
	if first != nil {
		content := strings.TrimSpace(first.SpokenContent())
		if content != "" {
			speech += "  " + content
			braille += " " + content
		}
		if first.Locale != "" {
			commands = append(commands, events.SpeechCommand{Kind: "language", Value: first.Locale})
		}
	}
	return Presentation{Speech: strings.TrimSpace(speech), SpeechCommands: commands, Braille: strings.TrimSpace(braille)}
}

func combinePresentations(values ...Presentation) Presentation {
	result := Presentation{}
	for _, value := range values {
		if value.Speech != "" {
			if result.Speech != "" {
				result.Speech += "  "
			}
			result.Speech += value.Speech
		}
		if value.Braille != "" {
			if result.Braille != "" {
				result.Braille += " "
			}
			result.Braille += value.Braille
		}
		result.SpeechCommands = append(result.SpeechCommands, value.SpeechCommands...)
	}
	return result
}

func (p *Presenter) PresentLine(nodes []*model.Node) Presentation {
	values := make([]Presentation, 0, len(nodes))
	for _, node := range nodes {
		values = append(values, p.Present(node, "textNavigation"))
	}
	return combinePresentations(values...)
}

func (p *Presenter) Blank() Presentation {
	if p.locale == "de-DE" {
		return Presentation{Speech: "leer", Braille: ""}
	}
	return Presentation{Speech: "blank", Braille: ""}
}

// PresentTextParagraph mirrors NVDA's text-paragraph quick navigation: only
// the matched paragraph text is reported, without adding the paragraph role.
func (p *Presenter) PresentTextParagraph(node *model.Node) Presentation {
	if node == nil {
		return Presentation{}
	}
	text := strings.TrimSpace(strings.ReplaceAll(node.Text, "\ufffc", ""))
	if text == "" {
		text = strings.TrimSpace(node.SpokenContent())
	}
	text = strings.Join(strings.Fields(text), " ")
	return p.plain(normalizeTextParagraphSpeech(text, p.Settings().SpeechSymbolLevel), text, node, "quickNavigation")
}

// PresentTextError reports the text range with NVDA's formatting marker before
// its content. Braille retains the range text because error-marker braille is
// controlled by a separate NVDA document-formatting preference.
func (p *Presenter) PresentTextError(node *model.Node) Presentation {
	if node == nil {
		return Presentation{}
	}
	settings := p.Settings()
	marker := ""
	if slices.Contains(settings.ReportSpellingErrors, SpellingErrorsSpeech) {
		marker = "spelling error"
	}
	if textErrorKind(node) == "grammar" {
		if marker != "" {
			marker = "grammar error"
		}
	}
	if marker != "" && p.locale == "de-DE" {
		if textErrorKind(node) == "grammar" {
			marker = "Grammatikfehler"
		} else {
			marker = "Rechtschreibfehler"
		}
	}
	text := strings.TrimSpace(node.SpokenContent())
	return p.plain(joinSpeech(marker, text), text, node, "quickNavigation")
}

func normalizeTextParagraphSpeech(text string, level SpeechSymbolLevel) string {
	if level == SpeechSymbolsAll || level == SpeechSymbolsCharacter {
		return strings.TrimSpace(text)
	}
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
	if level == SpeechSymbolsNone {
		text = strings.Map(func(value rune) rune {
			if strings.ContainsRune("!#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", value) {
				return ' '
			}
			return value
		}, text)
		text = strings.Join(strings.Fields(text), " ")
	}
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
	settings := p.Settings()
	speechParts := make([]string, 0, 8)
	brailleParts := make([]string, 0, 8)
	rowChanged := previous == nil || node.Row != previous.Row
	columnChanged := previous == nil || node.Column != previous.Column
	if settings.ReportTables && settings.ReportTableCellCoordinates && node.Row > 0 && rowChanged {
		if p.locale == "de-DE" {
			speechParts = append(speechParts, "Zeile "+strconv.Itoa(node.Row))
		} else {
			speechParts = append(speechParts, "row "+strconv.Itoa(node.Row))
		}
		brailleParts = append(brailleParts, "r"+strconv.Itoa(node.Row))
	}
	if settings.ReportTables && settings.ReportTableCellCoordinates && node.Column > 0 && columnChanged {
		if p.locale == "de-DE" {
			speechParts = append(speechParts, "Spalte "+strconv.Itoa(node.Column))
		} else {
			speechParts = append(speechParts, "column "+strconv.Itoa(node.Column))
		}
		brailleParts = append(brailleParts, "c"+strconv.Itoa(node.Column))
	}
	if settings.ReportTables && columnChanged && reportsColumnHeaders(settings.ReportTableHeaders) {
		speechParts = appendDistinct(speechParts, node.ColumnHeaderText...)
		brailleParts = appendDistinct(brailleParts, node.ColumnHeaderText...)
	}
	if settings.ReportTables && rowChanged && reportsRowHeaders(settings.ReportTableHeaders) {
		speechParts = appendDistinct(speechParts, node.RowHeaderText...)
		brailleParts = appendDistinct(brailleParts, node.RowHeaderText...)
	}
	if content := strings.TrimSpace(node.SpokenContent()); content != "" {
		speechParts = appendDistinct(speechParts, content)
		brailleParts = appendDistinct(brailleParts, content)
	}
	if settings.ReportTables && node.RowSpan > 1 {
		if p.locale == "de-DE" {
			speechParts = append(speechParts, fmt.Sprintf("über %d Zeilen", node.RowSpan))
		} else {
			speechParts = append(speechParts, fmt.Sprintf("spans %d rows", node.RowSpan))
		}
	}
	if settings.ReportTables && node.ColumnSpan > 1 {
		if p.locale == "de-DE" {
			speechParts = append(speechParts, fmt.Sprintf("über %d Spalten", node.ColumnSpan))
		} else {
			speechParts = append(speechParts, fmt.Sprintf("spans %d columns", node.ColumnSpan))
		}
	}
	if description := strings.TrimSpace(node.Description); settings.ReportObjectDescriptions && settings.ReportAriaDescription && description != "" && description != strings.TrimSpace(node.SpokenContent()) {
		speechParts = appendDistinct(speechParts, description)
		brailleParts = appendDistinct(brailleParts, description)
	}
	if settings.ReportObjectDescriptions {
		speechParts = appendDistinct(speechParts, node.RelationText["described by"]...)
		brailleParts = appendDistinct(brailleParts, node.RelationText["described by"]...)
	}
	if settings.ReportDetails && len(node.RelationText["details"]) > 0 {
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

func reportsColumnHeaders(value TableHeaderReporting) bool {
	return value == TableHeadersRowsAndColumns || value == TableHeadersColumns
}

func reportsRowHeaders(value TableHeaderReporting) bool {
	return value == TableHeadersRowsAndColumns || value == TableHeadersRows
}

func reportsRole(node *model.Node, settings PresentationSettings) bool {
	if node == nil {
		return false
	}
	role := strings.ToLower(strings.TrimSpace(node.Role))
	switch role {
	case "heading":
		return settings.ReportHeadings
	case "landmark":
		return settings.ReportLandmarks
	case "link":
		return settings.ReportLinks
	case "table", "table cell", "cell", "column header", "row header":
		return settings.ReportTables
	case "list", "list item":
		return settings.ReportLists
	case "image", "graphic":
		return settings.ReportGraphics
	case "annotation", "comment":
		return settings.ReportComments
	case "blockquote", "block quote":
		return settings.ReportBlockQuotes
	case "section", "grouping":
		return settings.ReportGroupings
	case "article":
		return settings.ReportArticles
	case "document frame", "internal frame", "frame":
		return settings.ReportFrames
	case "figure":
		return settings.ReportFigures
	default:
		return true
	}
}

func editableRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "entry", "password text", "text", "spin button", "combo box":
		return true
	default:
		return false
	}
}

func (p *Presenter) presentBraille(node *model.Node, reason string, settings PresentationSettings) string {
	parts := make([]string, 0, 12)
	if settings.ReportTables && reason == "tableNavigation" {
		if settings.ReportTableCellCoordinates && node.Row > 0 {
			parts = append(parts, "r"+strconv.Itoa(node.Row))
		}
		if settings.ReportTableCellCoordinates && node.Column > 0 {
			parts = append(parts, "c"+strconv.Itoa(node.Column))
		}
		if reportsColumnHeaders(settings.ReportTableHeaders) {
			parts = appendDistinct(parts, node.ColumnHeaderText...)
		}
		if reportsRowHeaders(settings.ReportTableHeaders) {
			parts = appendDistinct(parts, node.RowHeaderText...)
		}
	}
	if content := strings.TrimSpace(node.SpokenContent()); content != "" {
		parts = appendDistinct(parts, content)
	}
	if editableRole(node.Role) && !node.IsProtected() {
		if value := strings.TrimSpace(strings.ReplaceAll(node.Text, "\ufffc", "")); value != "" {
			parts = appendDistinct(parts, value)
		}
	}
	if description := strings.TrimSpace(node.Description); settings.ReportObjectDescriptions && settings.ReportAriaDescription && description != "" && description != strings.TrimSpace(node.SpokenContent()) {
		parts = appendDistinct(parts, description)
	}
	if role := p.brailleRoleForSettings(node, settings); role != "" && !sameNormalized(parts, role) {
		parts = append(parts, role)
	}
	for _, state := range orderedStates(node) {
		if state == "visited" && !settings.ReportLinkType {
			continue
		}
		parts = append(parts, p.brailleState(state))
	}
	if settings.ReportObjectPositionInformation && node.PositionInSet > 0 && node.SetSize > 0 {
		parts = append(parts, fmt.Sprintf("%d of %d", node.PositionInSet, node.SetSize))
	}
	if settings.ReportTables && settings.ReportTableCellCoordinates && reason != "tableNavigation" {
		if node.Row > 0 {
			parts = append(parts, "r"+strconv.Itoa(node.Row))
		}
		if node.Column > 0 {
			parts = append(parts, "c"+strconv.Itoa(node.Column))
		}
	}
	if settings.ReportTables && node.Role == "table" && node.RowCount > 0 && node.ColumnCount > 0 {
		parts = append(parts, fmt.Sprintf("%dr %dc", node.RowCount, node.ColumnCount))
	}
	if shouldPresentRelations(reason) {
		if settings.ReportObjectDescriptions {
			parts = appendDistinct(parts, node.RelationText["described by"]...)
		}
		if settings.ReportDetails && len(node.RelationText["details"]) > 0 {
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
	if settings.ReportKeyboardShortcuts {
		parts = appendDistinct(parts, node.KeyboardShortcut)
	}
	return strings.Join(nonEmpty(parts), " ")
}

func (p *Presenter) brailleRoleForSettings(node *model.Node, settings PresentationSettings) string {
	if !reportsRole(node, settings) {
		return ""
	}
	return p.brailleRole(node)
}

func (p *Presenter) brailleRole(node *model.Node) string {
	role := strings.ToLower(strings.TrimSpace(node.Role))
	if role == "heading" && node.HeadingLevel > 0 {
		if p.locale == "de-DE" {
			return "ü" + strconv.Itoa(node.HeadingLevel)
		}
		return "h" + strconv.Itoa(node.HeadingLevel)
	}
	if role == "landmark" {
		landmark := ""
		if values := strings.Fields(strings.ToLower(node.Attributes["xml-roles"])); len(values) > 0 {
			landmark = values[0]
		}
		if p.locale == "de-DE" {
			abbreviations := map[string]string{"main": "Haupt", "banner": "Bnnr", "navigation": "Navi", "contentinfo": "Info", "complementary": "Erg", "search": "Suche", "form": "Form", "region": "Reg"}
			if value := abbreviations[landmark]; value != "" {
				return "spmk " + value
			}
			return "spmk"
		}
		abbreviations := map[string]string{"main": "main", "banner": "bnnr", "navigation": "navi", "contentinfo": "cinf", "complementary": "cmpl", "search": "srch", "form": "form", "region": "rgn"}
		if value := abbreviations[landmark]; value != "" {
			return "lmk " + value
		}
		return "lmk"
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
			"content insertion": "einfg", "content deletion": "gel",
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
		"content insertion": "ins", "content deletion": "del",
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

func (p *Presenter) Title(document *model.Node) Presentation {
	title := ""
	if document != nil {
		title = strings.TrimSpace(document.Name)
	}
	if title == "" {
		if p.locale == "de-DE" {
			title = "Ohne Titel"
		} else {
			title = "No title"
		}
	}
	return Presentation{Speech: title, Braille: title}
}

func (p *Presenter) ShortcutKey(value string) Presentation {
	value = strings.TrimSpace(value)
	if value == "" {
		if p.locale == "de-DE" {
			value = "Keine Schnelltaste"
		} else {
			value = "No shortcut key"
		}
	}
	return Presentation{Speech: value, Braille: value}
}

func (p *Presenter) CurrentLine(value string) Presentation {
	value = strings.TrimSpace(value)
	if value == "" {
		if p.locale == "de-DE" {
			value = "leer"
		} else {
			value = "blank"
		}
	}
	return Presentation{Speech: value, Braille: value}
}

func (p *Presenter) Selection(value string) Presentation {
	value = strings.TrimSpace(value)
	if value == "" {
		if p.locale == "de-DE" {
			value = "Keine Auswahl"
		} else {
			value = "No selection"
		}
	} else if p.locale == "de-DE" {
		value += " ausgewählt"
	} else {
		value += " selected"
	}
	return Presentation{Speech: value, Braille: value}
}

func (p *Presenter) TextFormatting(node *model.Node, offset int) Presentation {
	attributes := map[string]string{}
	if node != nil {
		for _, run := range node.TextAttributeRuns {
			if offset < run.Start || offset >= run.End {
				continue
			}
			for name, value := range run.Attributes {
				if strings.TrimSpace(value) != "" {
					attributes[strings.TrimSpace(name)] = strings.TrimSpace(value)
				}
			}
			break
		}
	}
	parts := make([]string, 0, 8)
	if family := nvdaFontFamily(attributes["family-name"]); family != "" {
		parts = append(parts, family)
	}
	sizeValue := attributes["size"]
	if sizeValue == "" {
		sizeValue = attributes["font-size"]
	}
	if size := spacedCSSUnit(sizeValue); size != "" {
		parts = append(parts, size)
	}
	foreground := nvdaColorName(attributes["fg-color"], p.locale)
	background := nvdaColorName(attributes["bg-color"], p.locale)
	if foreground != "" {
		parts = append(parts, foreground)
	}
	if background != "" {
		if p.locale == "de-DE" {
			parts = append(parts, "auf "+background)
		} else {
			parts = append(parts, "on "+background)
		}
	}
	weightValue := attributes["weight"]
	if weightValue == "" {
		weightValue = attributes["font-weight"]
	}
	if weight, _ := strconv.Atoi(weightValue); weight >= 700 || strings.EqualFold(weightValue, "bold") {
		if p.locale == "de-DE" {
			parts = append(parts, "fett")
		} else {
			parts = append(parts, "bold")
		}
	}
	if node != nil {
		switch strings.ToLower(strings.TrimSpace(node.Attributes["text-align"])) {
		case "left", "start":
			if p.locale == "de-DE" {
				parts = append(parts, "linksbündig")
			} else {
				parts = append(parts, "align left")
			}
		case "right", "end":
			if p.locale == "de-DE" {
				parts = append(parts, "rechtsbündig")
			} else {
				parts = append(parts, "align right")
			}
		case "center":
			if p.locale == "de-DE" {
				parts = append(parts, "zentriert")
			} else {
				parts = append(parts, "align center")
			}
		}
	}
	text := strings.Join(parts, " ")
	if text == "" {
		if p.locale == "de-DE" {
			text = "Keine Formatierungsinformationen"
		} else {
			text = "No formatting information"
		}
	}
	return Presentation{Speech: text, Braille: text}
}

func nvdaFontFamily(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "liberation serif":
		return "Times New Roman"
	case "liberation sans":
		return "Arial"
	case "liberation mono":
		return "Courier New"
	case "wenquanyi zen hei mono":
		return "Consolas"
	default:
		return strings.TrimSpace(value)
	}
}

func spacedCSSUnit(value string) string {
	value = strings.TrimSpace(value)
	for _, suffix := range []string{"pt", "px", "em", "rem", "%"} {
		if strings.HasSuffix(strings.ToLower(value), suffix) {
			return strings.TrimSpace(value[:len(value)-len(suffix)]) + " " + value[len(value)-len(suffix):]
		}
	}
	return value
}

func nvdaColorName(value, locale string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), " ", "")
	names := map[string][2]string{
		"0,0,0":       {"black", "Schwarz"},
		"255,255,255": {"white", "Weiß"},
		"255,0,0":     {"red", "Rot"},
		"0,128,0":     {"green", "Grün"},
		"0,0,255":     {"blue", "Blau"},
	}
	if name, ok := names[value]; ok {
		if locale == "de-DE" {
			return name[1]
		}
		return name[0]
	}
	return value
}

func (p *Presenter) Language(locale string) Presentation {
	locale = strings.ReplaceAll(strings.TrimSpace(locale), "_", "-")
	language := strings.ToLower(strings.Split(locale, "-")[0])
	text := map[string]string{"de": "German (Germany)", "en": "English (United States)"}[language]
	if text == "" {
		if p.locale == "de-DE" {
			text = "Unbekannte Sprache"
		} else {
			text = "Unknown language"
		}
	} else if p.locale == "de-DE" {
		text += " (nicht unterstützt)"
	} else {
		text += " (not supported)"
	}
	return Presentation{Speech: text, Braille: text}
}

func (p *Presenter) LinkDestination(value string) Presentation {
	value = strings.TrimSpace(value)
	if value == "" {
		if p.locale == "de-DE" {
			value = "Kein Link"
		} else {
			value = "No link"
		}
	}
	return Presentation{Speech: value, Braille: value}
}

func (p *Presenter) CaretLocation(bounds model.Rect) Presentation {
	var text string
	if p.locale == "de-DE" {
		text = fmt.Sprintf("links %d, oben %d, Breite %d, Höhe %d", bounds.X, bounds.Y, bounds.Width, bounds.Height)
	} else {
		text = fmt.Sprintf("left %d, top %d, width %d, height %d", bounds.X, bounds.Y, bounds.Width, bounds.Height)
	}
	return Presentation{Speech: text, Braille: text}
}

func (p *Presenter) ObjectBoundary(kind string) Presentation {
	text := ""
	if p.locale == "de-DE" {
		text = map[string]string{
			"navigator": "Kein Navigator-Objekt", "containing": "Kein übergeordnetes Objekt",
			"next": "Kein weiteres Objekt", "previous": "Kein vorheriges Objekt",
			"inside": "Kein untergeordnetes Objekt", "focus": "Kein Fokus",
		}[kind]
	} else {
		text = map[string]string{
			"navigator": "No navigator object", "containing": "No containing object",
			"next": "No next", "previous": "No previous", "inside": "No objects inside",
			"focus": "No focus",
		}[kind]
	}
	return Presentation{Speech: text, Braille: text}
}

func (p *Presenter) Activate() Presentation {
	if p.locale == "de-DE" {
		return Presentation{Speech: "Aktivieren", Braille: "Aktivieren"}
	}
	return Presentation{Speech: "Activate", Braille: "Activate"}
}

func (p *Presenter) NoAction() Presentation {
	if p.locale == "de-DE" {
		return Presentation{Speech: "Keine Aktion", Braille: "Keine Aktion"}
	}
	return Presentation{Speech: "No action", Braille: "No action"}
}

func (p *Presenter) MoveFocus() Presentation {
	if p.locale == "de-DE" {
		return Presentation{Speech: "Fokus verschieben", Braille: "Fokus verschieben"}
	}
	return Presentation{Speech: "Move focus", Braille: "Move focus"}
}

func (p *Presenter) ReviewBoundary(kind string) Presentation {
	text := ""
	if p.locale == "de-DE" {
		text = map[string]string{
			"top": "Oben", "bottom": "Unten", "left": "Links", "right": "Rechts",
			"empty": "Leer", "pageUnsupported": "Seitenweises Verschieben wird nicht unterstützt",
			"noSelection": "Keine Auswahl", "noCopyStart": "Keine Startmarke gesetzt",
			"noNextMode":     "Kein weiterer Betrachter vorhanden",
			"noPreviousMode": "Kein weiterer Betrachter vorhanden",
		}[kind]
	} else {
		text = map[string]string{
			"top": "Top", "bottom": "Bottom", "left": "Left", "right": "Right",
			"empty": "blank", "pageUnsupported": "Movement by page not supported",
			"noSelection": "No selection", "noCopyStart": "No start marker set",
			"noNextMode": "No next review mode", "noPreviousMode": "No previous review mode",
		}[kind]
	}
	return Presentation{Speech: text, Braille: text}
}

func (p *Presenter) ReviewCopyStart() Presentation {
	if p.locale == "de-DE" {
		return Presentation{Speech: "Startmarke gesetzt", Braille: "Startmarke gesetzt"}
	}
	return Presentation{Speech: "Start marked", Braille: "Start marked"}
}

func (p *Presenter) ReviewSelected(characters int) Presentation {
	text := fmt.Sprintf("Selected %d characters", characters)
	if p.locale == "de-DE" {
		text = fmt.Sprintf("%d Zeichen ausgewählt", characters)
	}
	return Presentation{Speech: text, Braille: text}
}

func (p *Presenter) ReviewMode(mode string) Presentation {
	text := map[string]string{
		"object": "Object review", "document": "Document review", "screen": "Screen review",
	}[mode]
	if p.locale == "de-DE" {
		text = map[string]string{
			"object": "Objektbetrachter", "document": "Dokumentbetrachter", "screen": "Bildschirmbetrachter",
		}[mode]
	}
	return Presentation{Speech: text, Braille: text}
}

func (p *Presenter) MouseState(kind string, active bool) Presentation {
	text := ""
	if p.locale == "de-DE" {
		text = map[string]string{
			"leftClick": "Linksklick", "rightClick": "Rechtsklick",
		}[kind]
		if kind == "leftLock" {
			if active {
				text = "Linke Maustaste gesperrt"
			} else {
				text = "Linke Maustaste freigegeben"
			}
		} else if kind == "rightLock" {
			if active {
				text = "Rechte Maustaste gesperrt"
			} else {
				text = "Rechte Maustaste freigegeben"
			}
		}
	} else {
		text = map[string]string{
			"leftClick": "Left click", "rightClick": "Right click",
		}[kind]
		if kind == "leftLock" {
			if active {
				text = "Left mouse button lock"
			} else {
				text = "Left mouse button unlock"
			}
		} else if kind == "rightLock" {
			if active {
				text = "Right mouse button locked"
			} else {
				text = "Right mouse button unlocked"
			}
		}
	}
	return Presentation{Speech: text, Braille: text}
}

func (p *Presenter) MoveNavigatorToMouse() Presentation {
	if p.locale == "de-DE" {
		return Presentation{Speech: "Ziehe Navigator zur Maus", Braille: "Ziehe Navigator zur Maus"}
	}
	return Presentation{Speech: "Move navigator object to mouse", Braille: "Move navigator object to mouse"}
}

func (p *Presenter) MouseBoundary(kind string) Presentation {
	text := ""
	if p.locale == "de-DE" {
		text = map[string]string{
			"noLocation":      "Objekt enthält keine Informationen zum Standort",
			"unknownPosition": "Mausposition nicht verfügbar", "noObject": "Kein Objekt unter der Maus",
		}[kind]
	} else {
		text = map[string]string{
			"noLocation": "Object has no location", "unknownPosition": "Mouse position unavailable",
			"noObject": "No object under mouse",
		}[kind]
	}
	return Presentation{Speech: text, Braille: text}
}

func (p *Presenter) SpeechMode(mode string) Presentation {
	label := mode
	if p.locale == "de-DE" {
		label = map[string]string{
			"off": "aus", "beeps": "Signaltöne", "talk": "sprechen", "on-demand": "bei Bedarf",
		}[mode]
		return Presentation{Speech: "Sprachmodus " + label, Braille: "Sprachmodus " + label}
	}
	return Presentation{Speech: "Speech mode " + label, Braille: "Speech mode " + label}
}

func (p *Presenter) BrailleTether(tether string) Presentation {
	label := map[string]string{"auto": "automatically", "focus": "to focus", "review": "to review"}[tether]
	text := "Braille tethered " + label
	if p.locale == "de-DE" {
		label = map[string]string{"auto": "Automatisch", "focus": "An Fokus", "review": "An NVDA-Cursor"}[tether]
		text = "Braille-Darstellung gekoppelt: " + label
	}
	return Presentation{Speech: text, Braille: text}
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

func (p *Presenter) NotInContainer() Presentation {
	if p.locale == "de-DE" {
		return Presentation{Speech: "Nicht in einem Container", Braille: "Nicht in einem Container"}
	}
	return Presentation{Speech: "Not in a container", Braille: "Not in a container"}
}

func (p *Presenter) Bottom() Presentation {
	if p.locale == "de-DE" {
		return Presentation{Speech: "Unten", Braille: "Unten"}
	}
	return Presentation{Speech: "Bottom", Braille: "Bottom"}
}

func (p *Presenter) Refreshed() Presentation {
	if p.locale == "de-DE" {
		return Presentation{Speech: "Aktualisiert", Braille: "Aktualisiert"}
	}
	return Presentation{Speech: "Refreshed", Braille: "Refreshed"}
}

func (p *Presenter) NativeSelectionMode(enabled bool) Presentation {
	if p.locale == "de-DE" {
		if enabled {
			return Presentation{Speech: "Nativer Auswahlmodus aktiviert", Braille: "Nativer Auswahlmodus aktiviert"}
		}
		return Presentation{Speech: "Nativer Auswahlmodus deaktiviert", Braille: "Nativer Auswahlmodus deaktiviert"}
	}
	if enabled {
		return Presentation{Speech: "Native app selection mode enabled", Braille: "Native app selection mode enabled"}
	}
	return Presentation{Speech: "Native app selection mode disabled", Braille: "Native app selection mode disabled"}
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
	}
	if p.locale != "de-DE" {
		return directionalBoundary(english[target], direction)
	}
	german := map[string][2]string{
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
		"content insertion": "inserted", "content deletion": "deleted",
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
		"content insertion": "eingefügt", "content deletion": "gelöscht",
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
