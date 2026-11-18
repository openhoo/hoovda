package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openhoo/hoovda/internal/model"
)

func TestBrailleReviewLeavesCaretAloneAndFailedCaretDoesNotAnnounceSuccess(t *testing.T) {
	doc := model.ObjectID{Bus: "app", Path: "/doc"}
	edit := model.ObjectID{Bus: "app", Path: "/edit"}
	g, err := model.NewGraph(doc, map[model.ObjectID]*model.Node{
		doc:  {ID: doc, Role: "document web", Children: []model.ObjectID{edit}},
		edit: {ID: edit, Parent: doc, Role: "entry", Text: "first\nsecond\nthird", CaretOffset: 7, States: map[string]bool{"editable": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	e, store := engineForGraph(t, g)
	denied := errors.New("caret rejected")
	e.access.(*fakeAccess).selectionErr = denied
	e.mu.Lock()
	e.focus = edit
	e.review = model.Cursor{Object: edit, Offset: 7}
	e.brailleTether = "review"
	e.mu.Unlock()
	before := store.Cursor()
	if err := e.ExecuteDirect(context.Background(), "braillePreviousLine"); err != nil {
		t.Fatal(err)
	}
	if got := brailleAfter(t, store, before); got != "first" {
		t.Fatalf("review line = %q", got)
	}
	e.mu.Lock()
	e.brailleTether = "focus"
	e.mu.Unlock()
	before = store.Cursor()
	if err := e.ExecuteDirect(context.Background(), "brailleNextLine"); !errors.Is(err, denied) {
		t.Fatalf("error = %v", err)
	}
	if got := brailleAfter(t, store, before); got != "" {
		t.Fatalf("announced an unconfirmed caret move: %q", got)
	}
}

func TestBackgroundDocumentLoadPreservesFocusedEditor(t *testing.T) {
	doc := model.ObjectID{Bus: "app", Path: "/doc"}
	edit := model.ObjectID{Bus: "app", Path: "/edit"}
	frame := model.ObjectID{Bus: "app", Path: "/frame"}
	g, err := model.NewGraph(doc, map[model.ObjectID]*model.Node{
		doc:   {ID: doc, Role: "document web", Children: []model.ObjectID{edit, frame}},
		edit:  {ID: edit, Parent: doc, Role: "entry", States: map[string]bool{"editable": true, "focused": true}},
		frame: {ID: frame, Parent: doc, Role: "document web"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := engineForGraph(t, g)
	e.mu.Lock()
	e.focus, e.graphDirty = edit, false
	e.mu.Unlock()
	e.handleNativeEvent(context.Background(), NativeEvent{Name: "org.a11y.atspi.Event.Document.LoadComplete", Source: frame})
	if got := e.State().Focus; got != edit {
		t.Fatalf("iframe load took the cursor from the focused edit: %v", got)
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if !e.graphDirty {
		t.Fatal("document load did not invalidate the graph")
	}
}

func TestBrailleLineReadsAdjacentBlockInsideFocusedEditor(t *testing.T) {
	for _, tc := range []struct{ action, want string }{
		{"braillePreviousLine", "Alpha beta"}, {"brailleNextLine", "Omega last"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			doc := model.ObjectID{Bus: "app", Path: "/doc"}
			edit := model.ObjectID{Bus: "app", Path: "/edit"}
			rows := []model.ObjectID{{Bus: "app", Path: "/a"}, {Bus: "app", Path: "/b"}, {Bus: "app", Path: "/c"}}
			nodes := map[model.ObjectID]*model.Node{
				doc: {ID: doc, Role: "document web", Children: []model.ObjectID{edit}},
				edit: {ID: edit, Parent: doc, Role: "entry", Text: "\ufffc\ufffc\ufffc", CaretOffset: 1,
					States: map[string]bool{"editable": true, "focusable": true}, Children: rows},
			}
			for i, text := range []string{"Alpha beta", "Gamma delta", "Omega last"} {
				nodes[rows[i]] = &model.Node{ID: rows[i], Parent: edit, Role: "section", Text: text,
					Attributes: map[string]string{"display": "block"}, States: map[string]bool{"editable": true}}
			}
			nodes[rows[1]].CaretOffset = 4
			g, err := model.NewGraph(doc, nodes, 1)
			if err != nil {
				t.Fatal(err)
			}
			e, store := engineForGraph(t, g)
			e.mu.Lock()
			e.focus, e.review = edit, model.Cursor{Object: edit}
			e.mu.Unlock()
			before := store.Cursor()
			if err := e.ExecuteDirect(context.Background(), tc.action); err != nil {
				t.Fatal(err)
			}
			if got := brailleAfter(t, store, before); got != tc.want {
				t.Fatalf("braille = %q; want %q", got, tc.want)
			}
			if got := speechAfter(t, store, before); got != "" {
				t.Fatalf("unexpected speech: %q", got)
			}
			target := rows[0]
			if tc.action == "brailleNextLine" {
				target = rows[2]
			}
			if got := e.State(); got.Review.Object != target || got.Review.Offset != 0 || got.Cursor.Object != target {
				t.Fatalf("wrong cursor after moving to %q: %v %v", tc.want, got.Review, got.Cursor)
			}
		})
	}
}

func TestBrailleLineUsesExactDocumentBlockBeforeNamedContainer(t *testing.T) {
	doc := model.ObjectID{Bus: "app", Path: "/doc"}
	group := model.ObjectID{Bus: "app", Path: "/group"}
	rows := []model.ObjectID{{Bus: "app", Path: "/first"}, {Bus: "app", Path: "/second"}, {Bus: "app", Path: "/last"}}
	nodes := map[model.ObjectID]*model.Node{
		doc:   {ID: doc, Role: "document web", Children: []model.ObjectID{group}},
		group: {ID: group, Parent: doc, Role: "section", Name: "Line navigation", Attributes: map[string]string{"display": "block"}, Children: rows},
	}
	for i, text := range []string{"Alpha beta", "Gamma delta", "Omega last"} {
		nodes[rows[i]] = &model.Node{ID: rows[i], Parent: group, Role: "section", Text: text, Attributes: map[string]string{"display": "block"}}
	}
	g, err := model.NewGraph(doc, nodes, 1)
	if err != nil {
		t.Fatal(err)
	}
	e, store := engineForGraph(t, g)
	e.mu.Lock()
	e.focus, e.review = rows[1], model.Cursor{Object: rows[1]}
	e.mu.Unlock()
	for _, step := range []struct {
		action string
		target model.ObjectID
		text   string
	}{
		{"braillePreviousLine", rows[0], "Alpha beta"}, {"brailleNextLine", rows[1], "Gamma delta"}, {"brailleNextLine", rows[2], "Omega last"},
	} {
		before := store.Cursor()
		if err := e.ExecuteDirect(context.Background(), step.action); err != nil {
			t.Fatal(err)
		}
		if got := e.State(); got.Review.Object != step.target || got.Focus != rows[1] {
			t.Fatalf("%s: review %v, physical focus %v", step.action, got.Review, got.Focus)
		}
		if got := brailleAfter(t, store, before); !strings.Contains(got, step.text) {
			t.Fatalf("%s: braille %q", step.action, got)
		}
	}
}
