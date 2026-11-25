package engine

import (
	"testing"

	"github.com/openhoo/hoovda/internal/model"
)

func TestCursorSelectsDocumentInActiveWindow(t *testing.T) {
	id := func(path string) model.ObjectID { return model.ObjectID{Bus: "chrome", Path: path} }
	app, front, back := id("/app"), id("/front"), id("/back")
	visible, stale := id("/front/doc"), id("/back/doc")
	for _, tc := range []struct {
		label  string
		active bool
		want   model.ObjectID
	}{
		{"active window overrides stale document focus", true, visible},
		{"document focus when no active window is reported", false, stale},
	} {
		t.Run(tc.label, func(t *testing.T) {
			g, err := model.NewGraph(app, map[model.ObjectID]*model.Node{
				app:     {ID: app, Role: "application", Children: []model.ObjectID{front, back}},
				front:   {ID: front, Parent: app, Role: "frame", Children: []model.ObjectID{visible}, States: map[string]bool{"active": tc.active, "showing": true}},
				back:    {ID: back, Parent: app, Role: "frame", Children: []model.ObjectID{stale}, States: map[string]bool{"showing": true}},
				visible: {ID: visible, Parent: front, Role: "document web", Name: "Editor", States: map[string]bool{"showing": true}},
				stale:   {ID: stale, Parent: back, Role: "document web", Name: "Editor", States: map[string]bool{"focused": true, "showing": true}},
			}, 1)
			if err != nil {
				t.Fatal(err)
			}
			if got := initialCursor(g); got != tc.want {
				t.Errorf("initial cursor = %v, want %v", got, tc.want)
			}
			if got, ok := matchingDocument(g, &model.Node{Name: "Editor"}); !ok || got != tc.want {
				t.Errorf("restored document = %v, %t; want %v", got, ok, tc.want)
			}
		})
	}
}
