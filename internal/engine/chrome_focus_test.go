package engine

import (
	"github.com/openhoo/hoovda/internal/model"
	"testing"
)

func TestBrowserStatusNameDoesNotDelayUnrelatedWebFocus(t *testing.T) {
	id := func(path string) model.ObjectID { return model.ObjectID{Bus: "app", Path: path} }
	root, frame, doc, link, bubble, status := id("/app"), id("/page-frame"), id("/page"), id("/recent"), id("/status-frame"), id("/url-label")
	for _, test := range []struct {
		name             string
		target           model.ObjectID
		referencesStatus bool
		reads            int
	}{{"unrelated web link", link, false, 1}, {"browser focus", status, false, 2}, {"explicit description from browser", link, true, 2}} {
		t.Run(test.name, func(t *testing.T) {
			nodes := map[model.ObjectID]*model.Node{
				root:  {ID: root, Role: "application", Children: []model.ObjectID{frame, bubble}},
				frame: {ID: frame, Role: "frame", Parent: root, Children: []model.ObjectID{doc}},
				doc:   {ID: doc, Role: "document web", Parent: frame, Children: []model.ObjectID{link}},
				link:  {ID: link, Role: "link", Name: "Recent", Parent: doc}, bubble: {ID: bubble, Role: "frame", Parent: root, Children: []model.ObjectID{status}},
				status: {ID: status, Role: "static", Name: "Status text", Parent: bubble},
			}
			if test.referencesStatus {
				nodes[link].Relations = map[string][]model.ObjectID{"described by": {status}}
			}
			graph, err := model.NewGraph(root, nodes, 1)
			if err != nil {
				t.Fatal(err)
			}
			e, access, store := nativeFocusEngine(t, graph, nil)
			access.events <- NativeEvent{Name: "org.a11y.atspi.Event.Object.PropertyChange", Source: status, Detail: "accessible-name", Value: "New URL"}
			ev := nativeFocusOutput(t, e, access, store, test.target)
			if focusSpeech(ev) == "" || access.graphReads != test.reads {
				t.Fatalf("focus %q, graph reads %d; want %d", focusSpeech(ev), access.graphReads, test.reads)
			}
		})
	}
}
