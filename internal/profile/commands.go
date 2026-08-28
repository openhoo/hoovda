package profile

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/openhoo/hoovda/internal/model"
)

type Command struct {
	ID             string   `json:"id"`
	Label          string   `json:"label"`
	Desktop        []string `json:"desktopGestures"`
	Laptop         []string `json:"laptopGestures"`
	Category       string   `json:"category"`
	Target         string   `json:"target,omitempty"`
	Direction      int      `json:"direction,omitempty"`
	ConsumesBrowse bool     `json:"consumesBrowse"`
}

var commands = []Command{
	{ID: "nextFocusable", Label: "Next focusable element", Desktop: []string{"tab"}, Laptop: []string{"tab"}, Category: "focus", Direction: 1},
	{ID: "previousFocusable", Label: "Previous focusable element", Desktop: []string{"shift+tab"}, Laptop: []string{"shift+tab"}, Category: "focus", Direction: -1},
	{ID: "activate", Label: "Activate current element", Desktop: []string{"enter"}, Laptop: []string{"enter"}, Category: "activation", ConsumesBrowse: true},
	{ID: "activateWithSpace", Label: "Activate current element with Space", Desktop: []string{"space"}, Laptop: []string{"space"}, Category: "activation", ConsumesBrowse: true},
	{ID: "escape", Label: "Escape current context", Desktop: []string{"escape"}, Laptop: []string{"escape"}, Category: "mode"},
	{ID: "returnToPage", Label: "Return focus to web page", Desktop: []string{"f6"}, Laptop: []string{"f6"}, Category: "focus"},
	quick("nextHeading", "Next heading", "h", "heading", 1),
	quick("previousHeading", "Previous heading", "shift+h", "heading", -1),
	quick("nextHeading1", "Next heading level 1", "1", "heading1", 1),
	quick("previousHeading1", "Previous heading level 1", "shift+1", "heading1", -1),
	quick("nextHeading2", "Next heading level 2", "2", "heading2", 1),
	quick("previousHeading2", "Previous heading level 2", "shift+2", "heading2", -1),
	quick("nextHeading3", "Next heading level 3", "3", "heading3", 1),
	quick("previousHeading3", "Previous heading level 3", "shift+3", "heading3", -1),
	quick("nextHeading4", "Next heading level 4", "4", "heading4", 1),
	quick("previousHeading4", "Previous heading level 4", "shift+4", "heading4", -1),
	quick("nextHeading5", "Next heading level 5", "5", "heading5", 1),
	quick("previousHeading5", "Previous heading level 5", "shift+5", "heading5", -1),
	quick("nextHeading6", "Next heading level 6", "6", "heading6", 1),
	quick("previousHeading6", "Previous heading level 6", "shift+6", "heading6", -1),
	quick("nextHeading7", "Next heading level 7", "7", "heading7", 1),
	quick("previousHeading7", "Previous heading level 7", "shift+7", "heading7", -1),
	quick("nextHeading8", "Next heading level 8", "8", "heading8", 1),
	quick("previousHeading8", "Previous heading level 8", "shift+8", "heading8", -1),
	quick("nextHeading9", "Next heading level 9", "9", "heading9", 1),
	quick("previousHeading9", "Previous heading level 9", "shift+9", "heading9", -1),
	quick("nextLandmark", "Next landmark", "d", "landmark", 1),
	quick("previousLandmark", "Previous landmark", "shift+d", "landmark", -1),
	quick("nextButton", "Next button", "b", "button", 1),
	quick("previousButton", "Previous button", "shift+b", "button", -1),
	quick("nextFormField", "Next form field", "f", "formField", 1),
	quick("previousFormField", "Previous form field", "shift+f", "formField", -1),
	quick("nextLink", "Next link", "k", "link", 1),
	quick("previousLink", "Previous link", "shift+k", "link", -1),
	quick("nextVisitedLink", "Next visited link", "v", "visitedLink", 1),
	quick("previousVisitedLink", "Previous visited link", "shift+v", "visitedLink", -1),
	quick("nextUnvisitedLink", "Next unvisited link", "u", "unvisitedLink", 1),
	quick("previousUnvisitedLink", "Previous unvisited link", "shift+u", "unvisitedLink", -1),
	quick("nextList", "Next list", "l", "list", 1),
	quick("previousList", "Previous list", "shift+l", "list", -1),
	quick("nextListItem", "Next list item", "i", "listItem", 1),
	quick("previousListItem", "Previous list item", "shift+i", "listItem", -1),
	quick("nextTable", "Next table", "t", "table", 1),
	quick("previousTable", "Previous table", "shift+t", "table", -1),
	quick("nextImage", "Next graphic", "g", "graphic", 1),
	quick("previousImage", "Previous graphic", "shift+g", "graphic", -1),
	quick("nextCheckbox", "Next check box", "x", "checkBox", 1),
	quick("previousCheckbox", "Previous check box", "shift+x", "checkBox", -1),
	quick("nextRadioButton", "Next radio button", "r", "radioButton", 1),
	quick("previousRadioButton", "Previous radio button", "shift+r", "radioButton", -1),
	quick("nextCombobox", "Next combo box", "c", "comboBox", 1),
	quick("previousCombobox", "Previous combo box", "shift+c", "comboBox", -1),
	quick("nextEntry", "Next edit field", "e", "edit", 1),
	quick("previousEntry", "Previous edit field", "shift+e", "edit", -1),
	quick("nextParagraph", "Next text paragraph", "p", "textParagraph", 1),
	quick("previousParagraph", "Previous text paragraph", "shift+p", "textParagraph", -1),
	quick("nextFrame", "Next frame", "m", "frame", 1),
	quick("previousFrame", "Previous frame", "shift+m", "frame", -1),
	quick("nextSeparator", "Next separator", "s", "separator", 1),
	quick("previousSeparator", "Previous separator", "shift+s", "separator", -1),
	quick("nextBlockQuote", "Next block quote", "q", "blockQuote", 1),
	quick("previousBlockQuote", "Previous block quote", "shift+q", "blockQuote", -1),
	quick("nextEmbeddedObject", "Next embedded object", "o", "embeddedObject", 1),
	quick("previousEmbeddedObject", "Previous embedded object", "shift+o", "embeddedObject", -1),
	quick("nextAnnotation", "Next annotation", "a", "annotation", 1),
	quick("previousAnnotation", "Previous annotation", "shift+a", "annotation", -1),
	quick("nextSpellingError", "Next spelling or grammar error", "w", "error", 1),
	quick("previousSpellingError", "Previous spelling or grammar error", "shift+w", "error", -1),
	quick("nextNotLinkBlock", "Next text after block of links", "n", "notLinkBlock", 1),
	quick("previousNotLinkBlock", "Previous text after block of links", "shift+n", "notLinkBlock", -1),
	{ID: "nextCharacter", Label: "Next character", Desktop: []string{"right"}, Laptop: []string{"right"}, Category: "text", Direction: 1, ConsumesBrowse: true},
	{ID: "previousCharacter", Label: "Previous character", Desktop: []string{"left"}, Laptop: []string{"left"}, Category: "text", Direction: -1, ConsumesBrowse: true},
	{ID: "nextWord", Label: "Next word", Desktop: []string{"ctrl+right"}, Laptop: []string{"ctrl+right"}, Category: "text", Direction: 1, ConsumesBrowse: true},
	{ID: "previousWord", Label: "Previous word", Desktop: []string{"ctrl+left"}, Laptop: []string{"ctrl+left"}, Category: "text", Direction: -1, ConsumesBrowse: true},
	{ID: "nextLine", Label: "Next line", Desktop: []string{"down"}, Laptop: []string{"down"}, Category: "text", Direction: 1, ConsumesBrowse: true},
	{ID: "previousLine", Label: "Previous line", Desktop: []string{"up"}, Laptop: []string{"up"}, Category: "text", Direction: -1, ConsumesBrowse: true},
	{ID: "nextParagraphText", Label: "Next paragraph by text", Desktop: []string{"ctrl+down"}, Laptop: []string{"ctrl+down"}, Category: "text", Direction: 1, ConsumesBrowse: true},
	{ID: "previousParagraphText", Label: "Previous paragraph by text", Desktop: []string{"ctrl+up"}, Laptop: []string{"ctrl+up"}, Category: "text", Direction: -1, ConsumesBrowse: true},
	{ID: "documentStart", Label: "Start of document", Desktop: []string{"ctrl+home"}, Laptop: []string{"ctrl+home"}, Category: "text", Direction: -1, ConsumesBrowse: true},
	{ID: "documentEnd", Label: "End of document", Desktop: []string{"ctrl+end"}, Laptop: []string{"ctrl+end"}, Category: "text", Direction: 1, ConsumesBrowse: true},
	{ID: "previousTableColumn", Label: "Previous table column", Desktop: []string{"ctrl+alt+left"}, Laptop: []string{"ctrl+alt+left"}, Category: "table", Direction: -1, ConsumesBrowse: true},
	{ID: "nextTableColumn", Label: "Next table column", Desktop: []string{"ctrl+alt+right"}, Laptop: []string{"ctrl+alt+right"}, Category: "table", Direction: 1, ConsumesBrowse: true},
	{ID: "previousTableRow", Label: "Previous table row", Desktop: []string{"ctrl+alt+up"}, Laptop: []string{"ctrl+alt+up"}, Category: "table", Direction: -1, ConsumesBrowse: true},
	{ID: "nextTableRow", Label: "Next table row", Desktop: []string{"ctrl+alt+down"}, Laptop: []string{"ctrl+alt+down"}, Category: "table", Direction: 1, ConsumesBrowse: true},
	{ID: "firstTableColumn", Label: "First table column", Desktop: []string{"ctrl+alt+home"}, Laptop: []string{"ctrl+alt+home"}, Category: "table", Direction: -1, ConsumesBrowse: true},
	{ID: "lastTableColumn", Label: "Last table column", Desktop: []string{"ctrl+alt+end"}, Laptop: []string{"ctrl+alt+end"}, Category: "table", Direction: 1, ConsumesBrowse: true},
	{ID: "firstTableRow", Label: "First table row", Desktop: []string{"ctrl+alt+pageup"}, Laptop: []string{"ctrl+alt+pageup"}, Category: "table", Direction: -1, ConsumesBrowse: true},
	{ID: "lastTableRow", Label: "Last table row", Desktop: []string{"ctrl+alt+pagedown"}, Laptop: []string{"ctrl+alt+pagedown"}, Category: "table", Direction: 1, ConsumesBrowse: true},
	{ID: "readCurrent", Label: "Read current location", Desktop: []string{"insert+tab"}, Laptop: []string{"capslock+tab"}, Category: "report", ConsumesBrowse: true},
	{ID: "reportDetails", Label: "Report details", Desktop: []string{"insert+d"}, Laptop: []string{"capslock+d"}, Category: "report", ConsumesBrowse: true},
	{ID: "sayAll", Label: "Read from current location", Desktop: []string{"insert+down"}, Laptop: []string{"capslock+a"}, Category: "report", ConsumesBrowse: true},
	{ID: "toggleFocusMode", Label: "Toggle browse or focus mode", Desktop: []string{"insert+space"}, Laptop: []string{"capslock+space"}, Category: "mode", ConsumesBrowse: true},
	{ID: "toggleSingleLetterNavigation", Label: "Toggle single letter navigation", Desktop: []string{"insert+shift+space"}, Laptop: []string{"capslock+shift+space"}, Category: "mode", ConsumesBrowse: true},
	{ID: "elementsList", Label: "Elements list", Desktop: []string{"insert+f7"}, Laptop: []string{"capslock+f7"}, Category: "dialog", ConsumesBrowse: true},
	{ID: "find", Label: "Find", Desktop: []string{"insert+ctrl+f"}, Laptop: []string{"capslock+ctrl+f"}, Category: "dialog", ConsumesBrowse: true},
	{ID: "findNext", Label: "Find next", Desktop: []string{"insert+f3"}, Laptop: []string{"capslock+f3"}, Category: "dialog", Direction: 1, ConsumesBrowse: true},
	{ID: "findPrevious", Label: "Find previous", Desktop: []string{"insert+shift+f3"}, Laptop: []string{"capslock+shift+f3"}, Category: "dialog", Direction: -1, ConsumesBrowse: true},
}

func quick(id, label, gesture, target string, direction int) Command {
	return Command{ID: id, Label: label, Desktop: []string{gesture}, Laptop: []string{gesture}, Category: "quickNavigation", Target: target, Direction: direction, ConsumesBrowse: true}
}

func Commands() []Command {
	result := make([]Command, len(commands))
	copy(result, commands)
	return result
}

func SupportedCommands() []Command {
	return Commands()
}

func SupportedCommandByID(id string) (Command, bool) {
	return CommandByID(id)
}

func CommandByID(id string) (Command, bool) {
	for _, command := range commands {
		if command.ID == id {
			return command, true
		}
	}
	return Command{}, false
}

func CommandByGesture(gesture, layout string) (Command, bool) {
	gesture = NormalizeGesture(gesture)
	for _, command := range commands {
		gestures := command.Desktop
		if layout == "laptop" {
			gestures = command.Laptop
		}
		for _, candidate := range gestures {
			if NormalizeGesture(candidate) == gesture {
				return command, true
			}
		}
	}
	return Command{}, false
}

func NormalizeGesture(gesture string) string {
	parts := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(gesture)), func(r rune) bool { return r == '+' || r == '-' })
	for index, part := range parts {
		switch part {
		case "control":
			parts[index] = "ctrl"
		case "return":
			parts[index] = "enter"
		case "esc":
			parts[index] = "escape"
		}
	}
	if len(parts) <= 1 {
		return strings.Join(parts, "+")
	}
	key := parts[len(parts)-1]
	modifiers := append([]string(nil), parts[:len(parts)-1]...)
	order := map[string]int{"ctrl": 0, "alt": 1, "shift": 2, "insert": 3, "capslock": 3}
	slices.SortStableFunc(modifiers, func(a, b string) int { return order[a] - order[b] })
	return strings.Join(append(modifiers, key), "+")
}

func MatchTarget(target string) func(*model.Node) bool {
	return func(node *model.Node) bool {
		role := strings.ToLower(node.Role)
		switch target {
		case "heading":
			return role == "heading"
		case "heading1", "heading2", "heading3", "heading4", "heading5", "heading6", "heading7", "heading8", "heading9":
			return role == "heading" && node.HeadingLevel == int(target[len(target)-1]-'0')
		case "landmark":
			if role == "landmark" {
				return true
			}
			for _, landmark := range strings.Fields(node.Attributes["xml-roles"]) {
				if slices.Contains([]string{"banner", "complementary", "contentinfo", "form", "main", "navigation", "region", "search"}, landmark) {
					return true
				}
			}
			return false
		case "button":
			return role == "push button" || role == "toggle button" || role == "button"
		case "formField":
			return slices.Contains([]string{"entry", "password text", "check box", "radio button", "combo box", "spin button", "slider", "button", "push button", "toggle button"}, role)
		case "link":
			return strings.Contains(role, "link")
		case "visitedLink":
			return strings.Contains(role, "link") && node.HasState("visited")
		case "unvisitedLink":
			return strings.Contains(role, "link") && !node.HasState("visited")
		case "list":
			return role == "list"
		case "listItem":
			return role == "list item"
		case "table":
			return role == "table"
		case "graphic":
			return role == "image" || role == "graphic"
		case "checkBox":
			return role == "check box"
		case "radioButton":
			return role == "radio button"
		case "comboBox":
			return role == "combo box"
		case "edit":
			return role == "entry" || role == "password text" || node.HasState("editable")
		case "textParagraph":
			return (role == "paragraph" || node.Attributes["tag"] == "p") && matchesTextParagraph(node.SpokenContent())
		case "frame":
			return role == "frame" || role == "internal frame"
		case "separator":
			return role == "separator"
		case "blockQuote":
			return role == "blockquote" || node.Attributes["tag"] == "blockquote"
		case "embeddedObject":
			return role == "embedded"
		case "annotation":
			return role == "annotation" || node.Attributes["xml-roles"] == "comment"
		case "error":
			return len(node.Children) == 0 && textErrorKind(node) != ""
		case "notLinkBlock":
			return role == "paragraph" && node.SpokenContent() != ""
		default:
			return false
		}
	}
}

func textErrorKind(node *model.Node) string {
	if kind := normalizedTextErrorKind(node.Attributes["invalid"]); kind != "" {
		return kind
	}
	if strings.EqualFold(node.Attributes["text-spelling"], "misspelled") {
		return "spelling"
	}
	for _, run := range node.TextAttributeRuns {
		for name, value := range run.Attributes {
			name = strings.ToLower(strings.TrimSpace(name))
			value = strings.ToLower(strings.TrimSpace(value))
			if name == "invalid" {
				if kind := normalizedTextErrorKind(value); kind != "" {
					return kind
				}
			}
			if strings.Contains(name, "spelling") && value != "" && value != "false" && value != "none" {
				return "spelling"
			}
			if strings.Contains(name, "grammar") && value != "" && value != "false" && value != "none" {
				return "grammar"
			}
		}
	}
	return ""
}

func normalizedTextErrorKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "spelling":
		return "spelling"
	case "grammar":
		return "grammar"
	default:
		return ""
	}
}

// matchesTextParagraph implements NVDA 2026.1.1's default text paragraph
// predicate from source/config/configDefaults.py without regex look-behind.
func matchesTextParagraph(text string) bool {
	runes := []rune(text)
	for index, value := range runes {
		if strings.ContainsRune("?!．！？：；", value) {
			return true
		}
		if value != '.' && value != '…' {
			continue
		}
		end := index
		for end+1 < len(runes) && end-index < 2 && (runes[end+1] == '.' || runes[end+1] == '…') {
			end++
		}
		before := index - 1
		if before >= 0 && strings.ContainsRune("\"”»)", runes[before]) {
			before--
		}
		if before < 0 || (!unicode.IsLetter(runes[before]) && runes[before] != '_') {
			continue
		}
		after := end + 1
		if after < len(runes) && strings.ContainsRune("\"”»)", runes[after]) {
			after++
		}
		for after < len(runes) && runes[after] == '[' {
			closing := after + 1
			for closing < len(runes) && unicode.IsDigit(runes[closing]) {
				closing++
			}
			if closing == after+1 || closing >= len(runes) || runes[closing] != ']' {
				break
			}
			after = closing + 1
		}
		if after == len(runes) || unicode.IsSpace(runes[after]) || runes[after] == '\u00a0' {
			return true
		}
	}
	return false
}

func ValidateCatalog() error {
	seenIDs := map[string]bool{}
	for _, command := range commands {
		if command.ID == "" || command.Label == "" {
			return fmt.Errorf("command has empty identity")
		}
		if seenIDs[command.ID] {
			return fmt.Errorf("duplicate command id %q", command.ID)
		}
		seenIDs[command.ID] = true
	}
	for _, layout := range []string{"desktop", "laptop"} {
		seenGestures := map[string]string{}
		for _, command := range commands {
			gestures := command.Desktop
			if layout == "laptop" {
				gestures = command.Laptop
			}
			for _, gesture := range gestures {
				normalized := NormalizeGesture(gesture)
				if existing, ok := seenGestures[normalized]; ok && existing != command.ID {
					return fmt.Errorf("%s gesture %q belongs to both %s and %s", layout, normalized, existing, command.ID)
				}
				seenGestures[normalized] = command.ID
			}
		}
	}
	return nil
}
