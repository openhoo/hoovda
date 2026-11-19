package engine

import (
	"context"
	"testing"

	"github.com/openhoo/hoovda/internal/model"
)

// Chromium can send document focus after a physical key is prepared but
// before the next AT-SPI graph contains that document.
func TestPhysicalDocumentStartWhileNewFrameIsNotInGraph(t *testing.T) {
	e, store := fixtureEngine(t)
	ctx := context.Background()
	access := e.access.(*fakeAccess)
	frame := model.ObjectID{Bus: "app", Path: "/new-frame-document"}
	access.readNodes = map[model.ObjectID]*model.Node{
		frame: {ID: frame, Role: "document web", Parent: access.graph.Root},
	}
	if err := e.BeginAction("test", "documentStart"); err != nil {
		t.Fatal(err)
	}
	defer e.EndAction("test", "documentStart")
	if err := e.PreparePhysicalCommand(ctx, "documentStart", "ctrl+home"); err != nil {
		t.Fatal(err)
	}
	e.handleNativeEvent(ctx, NativeEvent{Name: "org.a11y.atspi.Event.Focus", Source: frame})
	if got := e.State().Focus; got != frame {
		t.Fatalf("the native document focus was lost: %v", got)
	}
	before, reads := store.Cursor(), access.graphReads
	consumed, err := e.HandlePhysicalGesture(ctx, "ctrl+home", true)
	if err != nil || !consumed {
		t.Fatalf("physical documentStart after frame focus: consumed=%v, error=%v", consumed, err)
	}
	if got := speechAfter(t, store, before); got != "Checkout  heading  level 1" || access.graphReads != reads {
		t.Fatalf("documentStart speech=%q; graph reads during physical delivery=%d", got, access.graphReads-reads)
	}
}

func TestNewTopLevelFocusFollowsGraphAndDoesNotOutliveLaterFocus(t *testing.T) {
	for _, superseded := range []bool{false, true} {
		e, _ := fixtureEngine(t)
		ctx := context.Background()
		access := e.access.(*fakeAccess)
		old := access.graph.Root
		app := model.ObjectID{Bus: "app", Path: "/application"}
		newDoc := model.ObjectID{Bus: "app", Path: "/other-page"}
		button := model.ObjectID{Bus: "app", Path: "/button"}
		newNode := &model.Node{ID: newDoc, Parent: app, Role: "document web", Name: "Other page"}
		access.readNodes = map[model.ObjectID]*model.Node{newDoc: newNode}
		e.handleFocus(ctx, newDoc)
		if superseded {
			e.handleFocus(ctx, button)
		}
		nodes := map[model.ObjectID]*model.Node{
			app:    {ID: app, Role: "application", Children: []model.ObjectID{old, newDoc}},
			newDoc: newNode,
		}
		for id, node := range access.graph.Nodes {
			copy := *node
			if id == old {
				copy.Parent = app
			}
			nodes[id] = &copy
		}
		graph, err := model.NewGraph(app, nodes, 2)
		if err != nil {
			t.Fatal(err)
		}
		access.graph = graph
		if err := e.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		want := newDoc
		if superseded {
			want = button
		}
		if got := e.State().Cursor.Object; got != want {
			t.Fatalf("superseded=%v: cursor=%v, want %v", superseded, got, want)
		}
	}
}
