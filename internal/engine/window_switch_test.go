package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
)

// A button in the previous page can remain a live accessible while Chromium
// has moved to an input in another tab or window. Its delayed focus signal
// must not be required to read the current page correctly.
func TestPreparedReadAfterForegroundDocumentSwitch(t *testing.T) {
	const previous = "Previous document sentinel"
	for _, tc := range []struct {
		name   string
		shared bool
		want   string
	}{
		{"windows", false, "Search technical reports"},
		{"tabs", true, "Search technical reports"},
		{"nested document in foreground page", false, "Search technical reports"},
		{"two active windows", false, previous},
		{"no active window", false, previous},
		{"only hidden pages", false, previous},
		{"two shown tabs", true, previous},
		{"two focused editors", false, previous},
		{"deliberate position in foreground", false, "Browse destination"},
		{"browser chrome owns focus", false, previous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, store := fixtureEngine(t)
			a := e.access.(*fakeAccess)
			ctx := context.Background()
			id := func(s string) model.ObjectID { return model.ObjectID{Bus: "app", Path: "/" + s} }
			app, win, other, doc, prev, entry, button, heading := id("app"), id("win"), id("other"), id("catalog"), id("fixture"), id("search"), id("old-button"), id("heading")
			graph := func(switched bool) *model.Graph {
				backFrame := other
				if tc.shared {
					backFrame = win
				}
				leaf := func(id, par model.ObjectID, role, name string, states map[string]bool) *model.Node {
					return &model.Node{ID: id, Parent: par, Role: role, Name: name, States: states}
				}
				nodes := map[model.ObjectID]*model.Node{
					app:   {ID: app, Role: "application", Children: []model.ObjectID{win}},
					win:   {ID: win, Parent: app, Role: "frame", States: map[string]bool{"active": switched || tc.shared}, Children: []model.ObjectID{doc}},
					doc:   {ID: doc, Parent: win, Role: "document web", Name: "Catalogue", States: map[string]bool{"showing": switched}, Children: []model.ObjectID{entry, heading}},
					prev:  {ID: prev, Parent: backFrame, Role: "document web", Name: "Braille fixture", States: map[string]bool{"showing": !switched}, Children: []model.ObjectID{button}},
					entry: leaf(entry, doc, "entry", "Search technical reports", map[string]bool{"editable": true, "focused": switched, "focusable": true}),
					// Chromium can retain per-window focus on the old object.
					button:  leaf(button, prev, "push button", "Previous document sentinel", map[string]bool{"focused": true, "focusable": true}),
					heading: leaf(heading, doc, "heading", "Browse destination", nil),
				}
				if tc.shared {
					nodes[win].Children = append(nodes[win].Children, prev)
				} else {
					nodes[app].Children = append(nodes[app].Children, other)
					nodes[other] = &model.Node{ID: other, Parent: app, Role: "frame", States: map[string]bool{"active": !switched}, Children: []model.ObjectID{prev}}
				}
				if switched {
					switch tc.name {
					case "no active window":
						nodes[win].States["active"] = false
					case "only hidden pages":
						nodes[doc].States["showing"] = false
					case "two active windows":
						nodes[other].States["active"] = true
					case "two shown tabs":
						nodes[prev].States["showing"] = true
					case "two focused editors":
						second := id("other-editor")
						nodes[doc].Children = append(nodes[doc].Children, second)
						nodes[second] = leaf(second, doc, "entry", "Other editor", map[string]bool{"focused": true, "editable": true})
					case "nested document in foreground page":
						inner := id("embedded-page")
						nodes[doc].Children[0] = inner
						nodes[inner] = &model.Node{ID: inner, Parent: doc, Role: "document frame", States: map[string]bool{"showing": true}, Children: []model.ObjectID{entry}}
						nodes[entry].Parent = inner
					}
				}
				g, err := model.NewGraph(app, nodes, 1)
				if err != nil {
					t.Fatal(err)
				}
				return g
			}
			a.graph = graph(false)
			if err := e.Refresh(ctx); err != nil {
				t.Fatal(err)
			}
			e.handleFocus(ctx, button)
			if s := e.State(); s.Cursor.Object != button {
				t.Fatalf("previous native focus was not established: %+v", s)
			}
			a.graph = graph(true)
			e.handleNativeEvent(ctx, NativeEvent{Name: "org.a11y.atspi.Event.Document.LoadComplete", Source: doc})
			// Native focus and the browse position may legitimately differ.
			switch tc.name {
			case "deliberate position in foreground":
				e.cursor = model.Cursor{Object: heading, Mode: "browse"}
			case "browser chrome owns focus":
				e.focus, e.webContentFocused = win, false
			}
			before := store.Cursor()
			if err := e.BeginAction("test", "readCurrent"); err != nil {
				t.Fatal(err)
			}
			if err := e.PreparePhysicalCommand(ctx, "readCurrent", "insert+tab"); err != nil {
				t.Fatal(err)
			}
			if _, err := e.HandlePhysicalGesture(ctx, "insert+tab", true); err != nil {
				t.Fatal(err)
			}
			e.EndAction("test", "readCurrent")
			out, _, _ := store.Snapshot(before, "test")
			for _, kind := range []events.Kind{events.KindSpeech, events.KindBraille} {
				var first string
				for _, v := range out {
					if v.Kind == kind && v.CausalCommand == "readCurrent" {
						first = v.Text
						break
					}
				}
				if !strings.Contains(first, tc.want) {
					t.Errorf("first %v = %q; want %q", kind, first, tc.want)
				}
			}
		})
	}
}
