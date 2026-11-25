package engine

import (
	"context"
	"slices"
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
