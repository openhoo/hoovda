package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
)

func TestPhysicalQuickNavigationRespectsBrowseModeAndSingleLetterNavigation(t *testing.T) {
	id := func(path string) model.ObjectID { return model.ObjectID{Bus: "app", Path: path} }
	document, first, middle, last := id("/document"), id("/first"), id("/middle"), id("/last")
	graph, err := model.NewGraph(document, map[model.ObjectID]*model.Node{
		document: {ID: document, Role: "document web", Children: []model.ObjectID{first, middle, last}},
		first:    {ID: first, Parent: document, Role: "radio button", Name: "First option", States: map[string]bool{"enabled": true, "focusable": true}},
		middle:   {ID: middle, Parent: document, Role: "radio button", Name: "Middle option", States: map[string]bool{"enabled": true, "focusable": true}},
		last:     {ID: last, Parent: document, Role: "radio button", Name: "Last option", States: map[string]bool{"enabled": true, "focusable": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name         string
		mode         string
		singleLetter bool
		consume      bool
	}{
		{name: "focus mode", mode: "focus", singleLetter: true},
		{name: "browse mode with single letter navigation disabled", mode: "browse"},
		{name: "browse mode", mode: "browse", singleLetter: true, consume: true},
	} {
		for _, gesture := range []struct {
			key       string
			commandID string
			target    model.ObjectID
		}{
			{key: "r", commandID: "nextRadioButton", target: last},
			{key: "shift+r", commandID: "previousRadioButton", target: first},
		} {
			t.Run(scenario.name+"/"+gesture.key, func(t *testing.T) {
				value, access, store := nativeFocusEngine(t, graph, nil)
				nativeFocusOutput(t, value, access, store, middle)
				if state := value.State(); state.Focus != middle || state.Cursor.Object != middle {
					t.Fatalf("native radio focus did not establish the starting position: %#v", state)
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if value.State().Cursor.Mode != scenario.mode {
					if err := value.ExecuteDirect(ctx, "toggleFocusMode"); err != nil {
						t.Fatal(err)
					}
				}
				if !scenario.singleLetter {
					if err := value.ExecuteDirect(ctx, "toggleSingleLetterNavigation"); err != nil {
						t.Fatal(err)
					}
				}
				if err := value.WaitForSynthesis(ctx); err != nil {
					t.Fatal(err)
				}
				before := value.State()
				if before.Cursor.Mode != scenario.mode || before.SingleLetterNavigation != scenario.singleLetter {
					t.Fatalf("quick navigation preconditions = %#v", before)
				}
				checkpoint := store.Cursor()
				consumeAtPress := value.ShouldConsumeGesture(gesture.key)
				if consumeAtPress != scenario.consume {
					t.Errorf("ShouldConsumeGesture(%q) = %v, want %v", gesture.key, consumeAtPress, scenario.consume)
				}
				// Physical device callbacks queue the handler even for unconsumed keys.
				consume, err := value.HandlePhysicalGesture(ctx, gesture.key, consumeAtPress)
				if err != nil {
					t.Fatal(err)
				}
				if consume != scenario.consume {
					t.Errorf("HandlePhysicalGesture(%q) consumed = %v, want %v", gesture.key, consume, scenario.consume)
				}
				if err := value.WaitForSynthesis(ctx); err != nil {
					t.Fatal(err)
				}
				after := value.State()
				wantCursor := before.Cursor
				if scenario.consume {
					wantCursor.Object, wantCursor.Offset = gesture.target, 0
				}
				if after.Cursor != wantCursor {
					t.Errorf("physical %q moved cursor to %#v, want %#v", gesture.key, after.Cursor, wantCursor)
				}
				if after.Focus != before.Focus || len(access.focused) != 0 || len(access.activated) != 0 {
					t.Errorf("physical quick navigation changed native focus or activated a control: focus=%#v focused=%#v activated=%#v", after.Focus, access.focused, access.activated)
				}
				items, _, err := store.Snapshot(checkpoint, "test")
				if err != nil {
					t.Fatal(err)
				}
				foundSpeech, foundBraille := false, false
				for _, item := range items {
					switch item.Kind {
					case events.KindSpeech, events.KindBraille, events.KindAudio:
						if !scenario.consume {
							t.Errorf("unconsumed physical %q emitted output: %#v", gesture.key, item)
							continue
						}
						if item.Kind == events.KindAudio {
							continue
						}
						if item.Source == nil || *item.Source != gesture.target || item.CausalCommand != gesture.commandID || !strings.Contains(item.Text, graph.Nodes[gesture.target].Name) {
							t.Errorf("physical %q output lost target attribution: %#v", gesture.key, item)
						}
						foundSpeech = foundSpeech || item.Kind == events.KindSpeech
						foundBraille = foundBraille || item.Kind == events.KindBraille
					}
				}
				if scenario.consume && (!foundSpeech || !foundBraille) {
					t.Errorf("physical %q did not present its browse target in speech and braille: %#v", gesture.key, items)
				}
			})
		}
	}
}
