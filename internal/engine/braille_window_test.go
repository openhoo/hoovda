package engine

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/openhoo/hoovda/internal/braille"
	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
	"github.com/openhoo/hoovda/internal/profile"
)

// Fixed many-to-one entries isolate routing from the translator itself.
type contractedFixture struct{ braille.Passthrough }

func (contractedFixture) TranslateMapped(ctx context.Context, s, locale string) (braille.Mapping, error) {
	m := braille.Mapping{}
	r := []rune(s)
	for i := 0; i < len(r); {
		count := 1
		cell := byte(r[i])
		for _, entry := range []struct {
			word string
			cell byte
		}{{"for", 63}, {"the", 46}, {"and", 47}, {"with", 62}} {
			if strings.HasPrefix(string(r[i:]), entry.word) {
				count = len(entry.word)
				cell = entry.cell
				break
			}
		}
		m.CellToRune = append(m.CellToRune, i)
		for j := 0; j < count; j++ {
			m.RuneToCell = append(m.RuneToCell, len(m.Cells))
		}
		m.Cells = append(m.Cells, cell)
		i += count
	}
	return m, nil
}
func lastBraille(t *testing.T, s *events.Store, after uint64) events.Event {
	t.Helper()
	items, _, err := s.Snapshot(after, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range items {
		if v.Kind == events.KindBraille {
			return v
		}
	}
	t.Fatal("missing braille output")
	return events.Event{}
}

func TestContractedBrailleRoutesDisplayedValueAndPreservesReviewTether(t *testing.T) {
	for _, tether := range []string{"focus", "review"} {
		t.Run(tether, func(t *testing.T) {
			id := model.ObjectID{Bus: "app", Path: "/edit"}
			n := &model.Node{ID: id, Role: "entry", Name: "Plan", Text: "the and for with", CaretOffset: 12, States: map[string]bool{"editable": true}}
			g, err := model.NewGraph(id, map[model.ObjectID]*model.Node{id: n}, 1)
			if err != nil {
				t.Fatal(err)
			}
			e, s := engineForGraph(t, g)
			e.braille = contractedFixture{}
			e.focus = id
			e.review = model.Cursor{Object: id}
			e.brailleTether = tether
			e.emit(profile.Presentation{Braille: "Plan the and for with edt"}, n, "readCurrent", "read", "normal")
			if tether == "focus" {
				v := lastBraille(t, s, 0)
				if v.BrailleCursor == nil || *v.BrailleCursor != 11 {
					t.Fatalf("missing or incorrect initial caret: %v", v.BrailleCursor)
				}
			}
			// Cell 1 is in the label and has no native offset; cell 9 begins "for".
			if err := e.ExecuteDirectWithArgument(context.Background(), "brailleRoute", "1"); err != nil {
				t.Fatal(err)
			}
			if n.CaretOffset != 12 {
				t.Fatal("label route moved native caret")
			}
			before := s.Cursor()
			if err := e.ExecuteDirectWithArgument(context.Background(), "brailleRoute", "9"); err != nil {
				t.Fatal(err)
			}
			if e.State().Review.Offset != 8 {
				t.Fatalf("review offset %d", e.State().Review.Offset)
			}
			want := 8
			if tether == "review" {
				want = 12
			}
			if n.CaretOffset != want {
				t.Fatalf("native caret %d want %d", n.CaretOffset, want)
			}
			v := lastBraille(t, s, before)
			if v.BrailleCursor == nil || *v.BrailleCursor != 4 {
				t.Fatalf("routed cursor: %v", v.BrailleCursor)
			}
			before = s.Cursor()
			if err := e.ExecuteDirectWithArgument(context.Background(), "brailleRoute", "0"); err != nil {
				t.Fatal(err)
			}
			if v := lastBraille(t, s, before); v.BrailleCursor == nil || *v.BrailleCursor != 0 {
				t.Fatal("cell zero was lost")
			}
		})
	}
}
func TestPanKeepsTranslatedCellsAndStaysOnLongLine(t *testing.T) {
	id := model.ObjectID{Bus: "app", Path: "/multi"}
	line := strings.Repeat("the ", 54) + "end"
	n := &model.Node{ID: id, Role: "entry", Text: line + "\nnext"}
	g, err := model.NewGraph(id, map[model.ObjectID]*model.Node{id: n}, 1)
	if err != nil {
		t.Fatal(err)
	}
	e, s := engineForGraph(t, g)
	m := contractedFixture{}
	e.braille = m
	e.focus = id
	e.review = model.Cursor{Object: id}
	mapped, _ := m.TranslateMapped(context.Background(), line, "en-US")
	for _, action := range []struct {
		cmd   string
		first int
	}{{"braillePanBack", 0}, {"braillePanForward", 40}, {"braillePanForward", 80}, {"braillePanBack", 40}} {
		before := s.Cursor()
		if err := e.ExecuteDirect(context.Background(), action.cmd); err != nil {
			t.Fatal(err)
		}
		v := lastBraille(t, s, before)
		expected := mapped.Cells[action.first:min(len(mapped.Cells), action.first+40)]
		if !slices.Equal(v.BrailleCells, expected) {
			t.Fatalf("%s: cells %v", action.cmd, v.BrailleCells)
		}
	}
	if err := e.ExecuteDirectWithArgument(context.Background(), "brailleRoute", "2"); err != nil {
		t.Fatal(err)
	}
	if pos := e.State().Review.Offset; pos != mapped.CellToRune[42] {
		t.Fatalf("routed %d, want %d", pos, mapped.CellToRune[42])
	}
}

func TestMultilineEditableCaretIndexesCompleteWireTranslation(t *testing.T) {
	id := model.ObjectID{Bus: "app", Path: "/multi"}
	n := &model.Node{ID: id, Role: "entry", Name: "Field", Text: "the and\n\nfor with", States: map[string]bool{"editable": true}}
	g, err := model.NewGraph(id, map[model.ObjectID]*model.Node{id: n}, 1)
	if err != nil {
		t.Fatal(err)
	}
	e, s := engineForGraph(t, g)
	e.braille = contractedFixture{}
	e.focus = id
	for _, tc := range []struct{ native, cell int }{{4, 8}, {9, 11}, {13, 13}} {
		n.CaretOffset = tc.native
		before := s.Cursor()
		e.emit(profile.Presentation{Braille: "Field " + n.Text + " edt"}, n, "readCurrent", "report", "normal")
		v := lastBraille(t, s, before)
		if !slices.Equal(v.BrailleCells, []byte{'F', 'i', 'e', 'l', 'd', ' ', 46, ' ', 47, 10, 10, 63, ' ', 62, ' ', 'e', 'd', 't'}) || v.BrailleCursor == nil || *v.BrailleCursor != tc.cell {
			t.Fatalf("native %d: got %v cursor %v, want %d", tc.native, v.BrailleCells, v.BrailleCursor, tc.cell)
		}
	}
}

func TestEmptyEditableBrailleDoesNotRouteLabelOrHint(t *testing.T) {
	for _, tc := range []struct{ name, label, hint string }{
		{"label", "Search", ""},
		{"placeholder name", "Type a search", "Type a search"},
		{"long label", strings.Repeat("Search ", 12) + "catalogue", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := model.ObjectID{Bus: "app", Path: "/edit"}
			n := &model.Node{ID: id, Role: "entry", Name: tc.label, States: map[string]bool{"editable": true}, Attributes: map[string]string{"placeholder-text": tc.hint}}
			g, err := model.NewGraph(id, map[model.ObjectID]*model.Node{id: n}, 1)
			if err != nil {
				t.Fatal(err)
			}
			e, s := engineForGraph(t, g)
			e.braille = contractedFixture{}
			e.focus = id
			e.review = model.Cursor{Object: id}
			presentation := e.presenter.Present(n, "read")
			e.emit(presentation, n, "readCurrent", "read", "normal")
			v := lastBraille(t, s, 0)
			if !strings.Contains(v.Text, tc.label) || len(v.BrailleCells) == 0 || v.BrailleCursor != nil {
				t.Errorf("empty field: text %q, %d cells, has cursor %v", v.Text, len(v.BrailleCells), v.BrailleCursor != nil)
			}
			if len(tc.label) > displayWidth {
				before := s.Cursor()
				if err := e.ExecuteDirect(context.Background(), "braillePanForward"); err != nil {
					t.Fatal(err)
				}
				pan := lastBraille(t, s, before)
				if !slices.Equal(pan.BrailleCells, v.BrailleCells[displayWidth:displayWidth*2]) || pan.BrailleCursor != nil || e.State().BrailleOffset != 0 {
					t.Errorf("panning the label: cells %v, cursor %v, source offset %d", pan.BrailleCells, pan.BrailleCursor, e.State().BrailleOffset)
				}
			}
			if err := e.ExecuteDirectWithArgument(context.Background(), "brailleRoute", "3"); err != nil {
				t.Fatal(err)
			}
			if n.CaretOffset != 0 || e.State().Review.Offset != 0 {
				t.Errorf("routing label moved caret to %d or review to %d; field is empty", n.CaretOffset, e.State().Review.Offset)
			}
		})
	}
}

func TestMultilineBrailleCommandsUseTheDisplayedFrame(t *testing.T) {
	// A focus frame includes a label, text from every line and a role.
	// Request the cell advertised for the native caret on the second line.
	for _, tc := range []struct{ cmd, label string }{
		{"brailleReportFormatting", "Field"},
		{"brailleRoute", "Field"},
		{"brailleRoute", strings.Repeat("Label ", 9)},
	} {
		t.Run(tc.cmd+"/"+tc.label, func(t *testing.T) {
			id := model.ObjectID{Bus: "app", Path: "/multi"}
			source := "the and\n\nfor with"
			n := &model.Node{ID: id, Role: "entry", Name: tc.label, Text: source, States: map[string]bool{"editable": true}, CaretOffset: 9, TextAttributeRuns: []model.TextAttributeRun{{Start: 9, End: 12, Attributes: map[string]string{"family-name": "Consolas", "weight": "700"}}}}
			g, err := model.NewGraph(id, map[model.ObjectID]*model.Node{id: n}, 1)
			if err != nil {
				t.Fatal(err)
			}
			e, s := engineForGraph(t, g)
			e.braille = contractedFixture{}
			e.focus = id
			e.review = model.Cursor{Object: id}
			e.emit(profile.Presentation{Braille: tc.label + " " + source + " edt"}, n, "readCurrent", "read", "normal")
			frame := lastBraille(t, s, 0)
			if frame.BrailleCursor == nil {
				t.Fatal("focus frame lacks a cursor")
			}
			// A label cell must never be treated as a native text cell.
			if err := e.ExecuteDirectWithArgument(context.Background(), tc.cmd, "0"); err != nil {
				t.Fatal(err)
			}
			if n.CaretOffset != 9 || e.State().Review.Offset != 0 {
				t.Fatal("label cell changed native or review offset")
			}
			before := s.Cursor()
			if err := e.ExecuteDirectWithArgument(context.Background(), tc.cmd, strconv.Itoa(*frame.BrailleCursor)); err != nil {
				t.Fatal(err)
			}
			switch tc.cmd {
			case "brailleReportFormatting":
				if got := lastBraille(t, s, before); !strings.Contains(got.Text, "Consolas") || !strings.Contains(got.Text, "bold") {
					t.Fatalf("second-line formatting: %+v", got)
				}
			case "brailleRoute":
				if e.State().Review.Offset != 9 || n.CaretOffset != 9 {
					t.Fatalf("routed displayed cell %d to review %d/native %d", *frame.BrailleCursor, e.State().Review.Offset, n.CaretOffset)
				}
				if got := lastBraille(t, s, before); got.Text != "for with" || got.BrailleCursor == nil || *got.BrailleCursor != 0 {
					t.Fatalf("wrong routed line: %+v", got)
				}
			}
		})
	}
}
