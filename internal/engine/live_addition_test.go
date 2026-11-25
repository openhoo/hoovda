package engine

import (
	"context"
	"reflect"
	"testing"

	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
)

func TestLiveEmbeddedAdditionsUseChangedChild(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/document"}
	owner := model.ObjectID{Bus: "app", Path: "/updates"}
	first := model.ObjectID{Bus: "app", Path: "/item1"}
	next := model.ObjectID{Bus: "app", Path: "/item2"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:  {ID: root, Role: "document web", Children: []model.ObjectID{owner}},
		owner: {ID: owner, Parent: root, Role: "section", Attributes: map[string]string{"live": "polite", "relevant": "additions"}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	e, store := engineForGraph(t, graph)
	ctx := context.Background()
	insert := func(child model.ObjectID) {
		container := graph.Nodes[owner]
		index := len(container.Children)
		container.Children = append(container.Children, child)
		container.Text += "\ufffc"
		graph.Nodes[child] = &model.Node{ID: child, Parent: owner, Role: "section", Text: "Saved", Attributes: map[string]string{"container-live": "polite", "container-relevant": "additions"}}
		// Chromium can publish the child's text before its parent's embedded-object
		// insertion. Its ChildrenChanged signal can omit the child reference.
		e.handleLiveTextChange(ctx, NativeEvent{Source: child, Detail: "insert", Detail2: 5, Value: "Saved"})
		e.handleLiveTextChange(ctx, NativeEvent{Source: owner, Detail: "insert", Detail1: int32(index), Detail2: 1, Value: "\ufffc"})
		e.handleLiveChildrenChange(ctx, NativeEvent{Source: owner, Detail: "add", Detail1: int32(index)})
	}
	checkpoint := store.Cursor()
	insert(first)
	insert(next)
	all, _, err := store.Snapshot(checkpoint, "test")
	if err != nil {
		t.Fatal(err)
	}
	var spoken []string
	for _, ev := range all {
		if ev.Kind == events.KindSpeech {
			spoken = append(spoken, ev.Text)
		}
	}
	if !reflect.DeepEqual(spoken, []string{"Saved", "Saved"}) {
		t.Fatalf("two equal sibling additions should each speak once: %q", spoken)
	}
}
