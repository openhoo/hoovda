package engine

import (
	"context"
	"testing"
	"time"

	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
)

func TestPhysicalActivationKeepsConsumptionDecisionAcrossNativeFocus(t *testing.T) {
	for _, passThrough := range []bool{true, false} {
		name := "consumed activation"
		if passThrough {
			name = "native activation changes focus before queued handler"
		}
		t.Run(name, func(t *testing.T) {
			id := func(path string) model.ObjectID { return model.ObjectID{Bus: "app", Path: path} }
			document, entry, closeButton := id("/document"), id("/submit-entry"), id("/dialog-close")
			graph, err := model.NewGraph(document, map[model.ObjectID]*model.Node{
				document:    {ID: document, Role: "document web", Children: []model.ObjectID{entry, closeButton}},
				entry:       {ID: entry, Parent: document, Role: "entry", Name: "Submit entry", States: map[string]bool{"enabled": true, "focusable": true, "editable": true}},
				closeButton: {ID: closeButton, Parent: document, Role: "push button", Name: "Close dialog", States: map[string]bool{"enabled": true, "focusable": true}},
			}, 1)
			if err != nil {
				t.Fatal(err)
			}
			value, access, store := nativeFocusEngine(t, graph, nil)
			if passThrough {
				nativeFocusOutput(t, value, access, store, entry)
			} else {
				nativeFocusOutput(t, value, access, store, closeButton)
			}
			consumeAtPress := value.ShouldConsumeGesture("enter")
			if consumeAtPress == passThrough {
				t.Fatalf("Enter consumption at press = %v, pass-through = %v", consumeAtPress, passThrough)
			}
			if passThrough {
				// Native Enter can open a dialog before the queued key-release handler.
				nativeFocusOutput(t, value, access, store, closeButton)
			}
			before := value.State()
			if before.Cursor.Mode != "browse" || before.Focus != closeButton {
				t.Fatalf("dialog focus before queued handler = %#v", before)
			}
			checkpoint := store.Cursor()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			consumed, err := value.HandlePhysicalGesture(ctx, "enter", consumeAtPress)
			if err != nil {
				t.Fatal(err)
			}
			if consumed != consumeAtPress {
				t.Errorf("queued Enter consumption = %v, decision at press = %v", consumed, consumeAtPress)
			}
			if passThrough && len(access.activated) != 0 {
				t.Errorf("passed-through Enter activated the newly focused dialog control again: %#v", access.activated)
			}
			if !passThrough && (len(access.activated) != 1 || access.activated[0] != closeButton) {
				t.Errorf("consumed Enter did not activate its control exactly once: %#v", access.activated)
			}
			if after := value.State(); after.Cursor != before.Cursor || after.Focus != before.Focus {
				t.Errorf("queued activation changed cursor or focus: before=%#v after=%#v", before, after)
			}
			items, _, err := store.Snapshot(checkpoint, "test")
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 2 || items[0].Kind != events.KindCommandStarted || items[1].Kind != events.KindCommandSettled || items[1].Reason != "completed" {
				t.Fatalf("physical activation lost its lifecycle or emitted output: %#v", items)
			}
			for _, item := range items {
				if item.CausalCommand != "activate" {
					t.Errorf("physical activation lifecycle lost command attribution: %#v", item)
				}
			}
		})
	}
}

func TestAutomaticFocusModeStopsApplicationAncestorsAtDocumentBoundary(t *testing.T) {
	for _, documentRole := range []string{"document web", "document frame"} {
		for _, inDocumentApplication := range []bool{false, true} {
			name := documentRole + "/plain button"
			if inDocumentApplication {
				name = documentRole + "/ARIA application button"
			}
			t.Run(name, func(t *testing.T) {
				id := func(path string) model.ObjectID { return model.ObjectID{Bus: "app", Path: path} }
				browser, document, application, entry, button := id("/browser"), id("/document"), id("/aria-app"), id("/entry"), id("/button")
				nodes := map[model.ObjectID]*model.Node{
					browser:  {ID: browser, Role: "application", Name: "Google Chrome for Testing", Children: []model.ObjectID{document}},
					document: {ID: document, Parent: browser, Role: documentRole, Children: []model.ObjectID{entry, button}},
					entry:    {ID: entry, Parent: document, Role: "entry", Name: "Name", States: map[string]bool{"enabled": true, "focusable": true, "editable": true}},
					button:   {ID: button, Parent: document, Role: "push button", Name: "Open dialog", States: map[string]bool{"enabled": true, "focusable": true}},
				}
				wantMode := "browse"
				if inDocumentApplication {
					nodes[document].Children = []model.ObjectID{entry, application}
					nodes[application] = &model.Node{ID: application, Parent: document, Role: "application", Name: "Web application", Children: []model.ObjectID{button}}
					nodes[button].Parent = application
					wantMode = "focus"
				}
				graph, err := model.NewGraph(browser, nodes, 1)
				if err != nil {
					t.Fatal(err)
				}
				value, access, store := nativeFocusEngine(t, graph, nil)
				nativeFocusOutput(t, value, access, store, entry)
				if mode := value.State().Cursor.Mode; mode != "focus" {
					t.Fatalf("editable control lost automatic focus mode: %q", mode)
				}
				nativeFocusOutput(t, value, access, store, button)
				if state := value.State(); state.Cursor.Mode != wantMode || state.Focus != button || state.Cursor.Object != button {
					t.Fatalf("native button state = %#v, want mode %q at button", state, wantMode)
				}
			})
		}
	}
}

func TestExpectedPhysicalGestureBoundToManagedAction(t *testing.T) {
	e := &Engine{activeSession: "test"}
	if err := e.BeginAction("test", "returnToPage"); err != nil {
		t.Fatal(err)
	}
	if e.ClaimPhysicalGesture("f6") {
		t.Fatal("beginning an action does not prove physical delivery")
	}
	if err := e.PreparePhysicalCommand(context.Background(), "returnToPage", "F6"); err != nil {
		t.Fatal(err)
	}
	if e.ClaimPhysicalGesture("shift+f6") || !e.ClaimPhysicalGesture("f6") || e.ClaimPhysicalGesture("f6") {
		t.Fatal("expected only the exact pending gesture")
	}
	e.EndAction("test", "returnToPage")
	if e.ClaimPhysicalGesture("f6") {
		t.Fatal("new input matched a completed action")
	}
}
