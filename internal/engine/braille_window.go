package engine

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/openhoo/hoovda/internal/braille"
	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
	"github.com/openhoo/hoovda/internal/profile"
)

const displayWidth = 40

// A window is over the translation of a full logical line. Never translate
// a page fragment: its first cell may be a continuation of a contraction.
type brailleWindow struct {
	object                          model.ObjectID
	source, line                    string
	sourceStart, displayStart, span int // rune offsets in the native text and presented line
	first                           int // index in the translated cells
}

// Routing into an editor uses offsets in AT-SPI Text. Its accessible name can
// still be displayed and panned when the text is empty, but isn't editable.
func brailleSource(node *model.Node) string {
	if node.HasState("editable") {
		return node.Text
	}
	return navigableText(node)
}

// A label, role or status on the display isn't a character in an editable field.
// Keep only unambiguous spans. Trimming by the presenter must not shift the native caret.
func presentedWindow(node *model.Node, line string) brailleWindow {
	w := brailleWindow{}
	if node == nil || node.IsProtected() || strings.ContainsRune(node.Text, '\ufffc') {
		return w
	}
	source := brailleSource(node)
	w.object, w.source, w.line = node.ID, source, line
	trimmed := strings.TrimSpace(source)
	if trimmed == "" || strings.Count(line, trimmed) != 1 {
		return w
	}
	i := strings.Index(line, trimmed)
	w.displayStart = utf8.RuneCountInString(line[:i])
	w.sourceStart = utf8.RuneCountInString(source) - utf8.RuneCountInString(strings.TrimLeftFunc(source, unicode.IsSpace))
	w.span = utf8.RuneCountInString(trimmed)
	return w
}

func nativeLine(node *model.Node, source string, line model.TextRange) brailleWindow {
	s, _ := line.Text(source)
	return brailleWindow{object: node.ID, source: source, line: s, sourceStart: line.Start, span: utf8.RuneCountInString(s)}
}

func mappedCursor(w brailleWindow, m braille.Mapping, native int) *int {
	if native < w.sourceStart || native > w.sourceStart+w.span {
		return nil
	}
	i := w.displayStart + native - w.sourceStart
	if i < 0 || i > len(m.RuneToCell) {
		return nil
	}
	cell := len(m.Cells)
	if i < len(m.RuneToCell) {
		cell = m.RuneToCell[i]
	}
	cell -= w.first
	if cell < 0 || cell >= displayWidth {
		return nil
	}
	return &cell
}

// lou_translate processes each physical line separately and preserves LF in
// its output. Joining the per-line maps preserves the same cells and makes
// caret positions after a newline meaningful in the complete wire buffer.
func mapBrailleBuffer(ctx context.Context, mapper braille.Mapper, text, locale string) (braille.Mapping, error) {
	if !strings.Contains(text, "\n") {
		return mapper.TranslateMapped(ctx, text, locale)
	}
	var result braille.Mapping
	for i, line := range strings.Split(text, "\n") {
		if err := ctx.Err(); err != nil {
			return braille.Mapping{}, err
		}
		if i > 0 {
			r, c := len(result.RuneToCell), len(result.Cells)
			result.CellToRune = append(result.CellToRune, r)
			result.RuneToCell = append(result.RuneToCell, c)
			result.Cells = append(result.Cells, '\n')
		}
		m, err := mapper.TranslateMapped(ctx, line, locale)
		if err != nil {
			return braille.Mapping{}, err
		}
		r, c := len(result.RuneToCell), len(result.Cells)
		for _, v := range m.CellToRune {
			result.CellToRune = append(result.CellToRune, r+v)
		}
		for _, v := range m.RuneToCell {
			result.RuneToCell = append(result.RuneToCell, c+v)
		}
		result.Cells = append(result.Cells, m.Cells...)
	}
	return result, nil
}

func (e *Engine) emitLogicalBraille(sessionID, text string, node *model.Node, command, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var cells []byte
	var cursor *int
	var id *model.ObjectID
	if node != nil {
		v := node.ID
		id = &v
	}
	w := presentedWindow(node, text)
	e.mu.Lock()
	focus, review, tether := e.focus, e.review, e.brailleTether
	if reason == "brailleLine" && node != nil && w.span == 0 && review.Object == node.ID && !node.IsProtected() {
		// Line navigation knows the exact native offset, even for duplicate lines.
		r, err := textRangeAt(node.ID, w.source, review.Offset, model.TextUnitLine)
		if err == nil {
			v := nativeLine(node, w.source, r)
			if v.line == text {
				w = v
			}
		}
	}
	e.brailleWindow = w
	e.brailleOffset = 0
	if reason == "brailleLine" {
		e.brailleOffset = w.sourceStart
	}
	e.mu.Unlock()
	if node != nil && node.ID == focus && node.HasState("editable") && !node.IsProtected() && tether != "review" && w.span > 0 {
		if mapper, ok := e.braille.(braille.Mapper); ok {
			if m, err := mapBrailleBuffer(ctx, mapper, text, e.cfg.Locale); err == nil {
				cells = m.Cells
				// Ordinary events contain the whole translation, not a panned window.
				i := node.CaretOffset - w.sourceStart + w.displayStart
				if node.CaretOffset >= w.sourceStart && node.CaretOffset <= w.sourceStart+w.span && i >= 0 && i <= len(m.RuneToCell) {
					c := len(cells)
					if i < len(m.RuneToCell) {
						c = m.RuneToCell[i]
					} else {
						cells = append(cells, 0)
					}
					cursor = &c
				}
			}
		}
	}
	if cells == nil {
		t, err := e.braille.Translate(ctx, text, e.cfg.Locale, 0)
		if err != nil {
			e.logger.Error("braille translation failed", "error", err)
		} else {
			cells = t.Cells
		}
	}
	e.store.Append(events.Event{Kind: events.KindBraille, SessionID: sessionID, CausalCommand: command, Source: id, Text: text, BrailleCells: cells, BrailleCursor: cursor, Reason: reason, Provenance: events.ProvenanceScreenReaderOutput, Redacted: node != nil && node.IsProtected()})
}

func (e *Engine) displayCommand(ctx context.Context, cmd profile.Command, arg string, node *model.Node, w brailleWindow, review model.Cursor, focus model.ObjectID, tether string) error {
	if node.IsProtected() || strings.ContainsRune(node.Text, '\ufffc') {
		return nil
	}
	mapper, ok := e.braille.(braille.Mapper)
	if !ok {
		return errors.New("braille position mapping unavailable")
	}
	cell := 0
	isPan := cmd.ID == "braillePanBack" || cmd.ID == "braillePanForward"
	if !isPan {
		if cmd.ID != "brailleRoute" && cmd.ID != "brailleReportFormatting" {
			return fmt.Errorf("unsupported braille control %q", cmd.ID)
		}
		if a := strings.TrimSpace(arg); a != "" {
			v, err := strconv.Atoi(a)
			if err != nil || v < 0 || v > 199 {
				return errors.New("braille cell must be an integer from 0 to 199")
			}
			cell = v
		}
	}
	source := brailleSource(node)
	if source == "" && (!isPan || !node.HasState("editable")) {
		return nil
	}
	edit := node.ID == focus && node.HasState("editable") && tether != "review"
	caret := review.Offset
	if edit {
		live, err := e.access.ReadNode(ctx, node.ID)
		if err != nil {
			return err
		}
		if live == nil || live.ID != node.ID || live.IsProtected() || live.Text != node.Text {
			return errors.New("editable braille source changed")
		}
		caret = live.CaretOffset
	}
	multiline := strings.Contains(source, "\n")
	if w.object != node.ID || w.source != source || strings.Contains(w.line, "\n") {
		if multiline {
			r, err := textRangeAt(node.ID, source, caret, model.TextUnitLine)
			if err != nil {
				return err
			}
			w = nativeLine(node, source, r)
		} else {
			w = presentedWindow(node, e.presenter.Present(node, "braille").Braille)
		}
	}
	// Object panning includes its role and state, even immediately after a
	// routing key displayed just the source text.
	if isPan && !multiline && w.line == source {
		object := presentedWindow(node, e.presenter.Present(node, "braille").Braille)
		if object.line != w.line && object.span > 0 {
			object.first = w.first
			w = object
		}
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	m, err := mapper.TranslateMapped(bounded, w.line, e.cfg.Locale)
	if err != nil {
		return fmt.Errorf("map braille line: %w", err)
	}
	if isPan {
		next := w.first + cmd.Direction*displayWidth
		if next >= 0 && next < len(m.Cells) {
			w.first = next
		} else if multiline {
			lines, err := model.TextUnitRanges(node.ID, source, model.TextUnitLine)
			if err != nil {
				return err
			}
			for i, r := range lines {
				if r.Start != w.sourceStart {
					continue
				}
				j := i + cmd.Direction
				if j >= 0 && j < len(lines) {
					w = nativeLine(node, source, lines[j])
					m, err = mapper.TranslateMapped(bounded, w.line, e.cfg.Locale)
					if err != nil {
						return err
					}
					if cmd.Direction < 0 {
						w.first = max(0, len(m.Cells)-1) / displayWidth * displayWidth
					}
				}
				break
			}
		}
	} else {
		pos := w.first + cell
		if cell >= displayWidth || pos >= len(m.CellToRune) {
			return nil
		}
		displayRune := m.CellToRune[pos]
		if displayRune < w.displayStart || displayRune >= w.displayStart+w.span {
			return nil
		}
		offset := w.sourceStart + displayRune - w.displayStart
		if cmd.ID == "brailleReportFormatting" {
			e.emit(e.presenter.TextFormatting(node, offset), node, cmd.ID, "brailleFormatting", "normal")
			return nil
		}
		if edit {
			if err = e.access.SetCaretOffset(ctx, node.ID, offset); err != nil {
				return fmt.Errorf("route editable braille caret: %w", err)
			}
		}
		e.mu.Lock()
		e.review.Object, e.review.Offset = node.ID, offset
		if tether != "review" {
			e.cursor.Object, e.cursor.Offset = node.ID, offset
		}
		e.mu.Unlock()
		caret = offset
		// Routing displays the source line, without synthesized role/label text.
		r, err := textRangeAt(node.ID, source, offset, model.TextUnitLine)
		if err != nil {
			return err
		}
		line := nativeLine(node, source, r)
		if line.line != w.line {
			m, err = mapper.TranslateMapped(bounded, line.line, e.cfg.Locale)
			if err != nil {
				return err
			}
		}
		w = line
		i := offset - w.sourceStart
		if i >= 0 && i < len(m.RuneToCell) {
			w.first = m.RuneToCell[i] / displayWidth * displayWidth
		}
	}
	start, end := w.first, min(w.first+displayWidth, len(m.Cells))
	if start < 0 || start > end || len(m.CellToRune) != len(m.Cells) {
		return errors.New("invalid braille display window")
	}
	runes := []rune(w.line)
	a, b := 0, len(runes)
	if start < end {
		a = m.CellToRune[start]
	}
	if end < len(m.Cells) {
		b = max(m.CellToRune[end], m.CellToRune[end-1]+1)
	}
	if a < 0 || b < a || b > len(runes) {
		return errors.New("invalid braille source span")
	}
	reason := "brailleRoute"
	if isPan {
		reason = "braillePan"
	}
	// Serialize the displayed cells with other announcements, like normal output.
	e.outputMu.Lock()
	defer e.outputMu.Unlock()
	e.mu.Lock()
	if e.outputsSealed {
		e.mu.Unlock()
		return nil
	}
	e.brailleWindow = w
	e.brailleOffset = w.sourceStart + min(w.span, max(0, a-w.displayStart))
	if isPan && multiline {
		e.review.Object, e.review.Offset = node.ID, w.sourceStart
	}
	session := e.activeSession
	e.mu.Unlock()
	var cursor *int
	if source != "" {
		cursor = mappedCursor(w, m, caret)
	}

	cells := m.Cells[start:end]
	// An insertion caret just beyond the final translated cell occupies a blank
	// display cell. This also makes empty visual lines navigable.
	if cursor != nil && *cursor == len(cells) && len(cells) < displayWidth {
		cells = append([]byte(nil), cells...)
		cells = append(cells, 0)
	}
	e.store.Append(events.Event{Kind: events.KindBraille, SessionID: session, CausalCommand: cmd.ID, Source: &node.ID, Text: string(runes[a:b]), BrailleCells: cells, BrailleCursor: cursor, Reason: reason, Provenance: events.ProvenanceScreenReaderOutput})
	return nil
}
