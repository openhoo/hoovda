package engine

import (
	"testing"

	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
)

func TestNativeSelectionInCollapsedSelect(t *testing.T) {
	id := func(path string) model.ObjectID { return model.ObjectID{Bus: "chrome", Path: "/" + path} }
	doc, combo, menu, previous, next, other := id("doc"), id("combo"), id("menu"), id("old"), id("next"), id("unrelated")
	graph, err := model.NewGraph(doc, map[model.ObjectID]*model.Node{
		doc: {ID: doc, Role: "document web", Children: []model.ObjectID{combo, other}},
		combo: {ID: combo, Parent: doc, Role: "combo box", Name: "Language", Attributes: map[string]string{"tag": "select"},
			States: map[string]bool{"focusable": true, "focused": true, "enabled": true}, Children: []model.ObjectID{menu}},
		menu:     {ID: menu, Parent: combo, Role: "menu", Attributes: map[string]string{"hidden": "true"}, Children: []model.ObjectID{previous, next}},
		previous: {ID: previous, Parent: menu, Role: "menu item", Name: "Choose", Attributes: map[string]string{"tag": "option"}},
		next:     {ID: next, Parent: menu, Role: "menu item", Name: "English", Attributes: map[string]string{"tag": "option"}, States: map[string]bool{"selected": true}},
		other:    {ID: other, Parent: doc, Role: "menu item", Name: "Unrelated", Attributes: map[string]string{"tag": "option"}, States: map[string]bool{"selected": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	value, access, store := nativeFocusEngine(t, graph, nil)
	nativeFocusOutput(t, value, access, store, combo)

	// Chromium keeps focus on the select. The selected state is reported on
	// the option beneath its popup, even while that popup is hidden.
	selected := func(source model.ObjectID, active int32) {
		value.handleNativeEvent(t.Context(), NativeEvent{Name: "org.a11y.atspi.Event.Object.StateChanged", Source: source, Detail: "selected", Detail1: active})
	}
	before := store.Cursor()
	value.handleNativeEvent(t.Context(), NativeEvent{Name: "org.a11y.atspi.Event.Object.SelectionChanged", Source: menu})
	selected(previous, 0)
	selected(other, 1)
	selected(next, 1)
	if err := value.WaitForSynthesis(t.Context()); err != nil {
		t.Fatal(err)
	}
	items, _, err := store.Snapshot(before, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []events.Kind{events.KindSpeech, events.KindBraille} {
		var outputs []events.Event
		for _, item := range items {
			if item.Kind == kind {
				outputs = append(outputs, item)
			}
		}
		if len(outputs) != 1 || outputs[0].Text != "English" || outputs[0].Source == nil || *outputs[0].Source != next {
			t.Errorf("%s after selecting English: %+v", kind, outputs)
		}
	}

	// A stale positive event must not describe an option which is no longer
	// in the live popup, belongs to an open popup, or is protected.
	for _, tc := range []struct {
		name   string
		target model.ObjectID
		change func(*model.Node)
	}{
		{"replaced options", menu, func(n *model.Node) { n.Children = []model.ObjectID{previous} }},
		{"expanded popup", combo, func(n *model.Node) { n.States = map[string]bool{"focused": true, "expanded": true} }},
		{"protected option", next, func(n *model.Node) { n.Redacted = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := *graph.Nodes[tc.target]
			tc.change(&live)
			access.readNodes = map[model.ObjectID]*model.Node{tc.target: &live}
			before := store.Cursor()
			selected(next, 1)
			items, _, err := store.Snapshot(before, "test")
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if item.Kind == events.KindSpeech || item.Kind == events.KindBraille {
					t.Errorf("unexpected presentation: %+v", item)
				}
			}
		})
	}
}
