package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/openhoo/hoovda/internal/engine"
	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
)

type delayedNativeCallbackInjector struct {
	engine    *engine.Engine
	access    *fakeAccess
	store     *events.Store
	sessionID string
	pageEntry model.ObjectID
	chromeTab model.ObjectID
	calls     int
	callbacks sync.WaitGroup
}

func (i *delayedNativeCallbackInjector) Press(ctx context.Context, gesture string) error {
	i.calls++
	target := i.pageEntry
	if i.calls > 1 {
		// A second physical F6 leaves the page that the first one reached.
		target = i.chromeTab
	}
	before := i.store.Cursor()
	i.access.events <- engine.NativeEvent{Name: "org.a11y.atspi.Event.Focus", Source: target}
	_, _, focused, err := i.store.WaitFor(ctx, before, i.sessionID, func(event events.Event) bool {
		return event.Kind == events.KindFocus && event.Source != nil && *event.Source == target
	})
	if err != nil || !focused {
		return fmt.Errorf("native F6 focus was not observed: focused=%v error=%v", focused, err)
	}
	i.callbacks.Add(1)
	go func() {
		defer i.callbacks.Done()
		// The unconsumed key already changed native focus. Its listener callback
		// can be delayed behind accessibility processing beyond the old 500 ms
		// acknowledgement window without losing the physical key.
		timer := time.NewTimer(600 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			_, _ = i.engine.HandlePhysicalGesture(ctx, gesture, false)
		}
	}()
	return nil
}

func TestPhysicalFocusActionDoesNotReplayKeyWhileNativeCallbackIsDelayed(t *testing.T) {
	server, access := testServerWithAccess(t)
	server.cfg.ActionTimeout = 3 * time.Second
	window := model.ObjectID{Bus: "app", Path: "/window"}
	document := model.ObjectID{Bus: "app", Path: "/contacts"}
	entry := model.ObjectID{Bus: "app", Path: "/email"}
	tab := model.ObjectID{Bus: "app", Path: "/chrome-tab"}
	graph, err := model.NewGraph(window, map[model.ObjectID]*model.Node{
		window:   {ID: window, Role: "frame", Children: []model.ObjectID{document, tab}},
		document: {ID: document, Parent: window, Role: "document web", Name: "Contacts", Children: []model.ObjectID{entry}},
		entry:    {ID: entry, Parent: document, Role: "entry", Name: "E-Mail-Adresse", States: map[string]bool{"enabled": true, "focusable": true, "editable": true}},
		tab:      {ID: tab, Parent: window, Role: "page tab", Name: "Contacts"},
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	access.graph = graph
	if err := server.engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	created := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "delayed-native-focus-callback"}, "test-secret")
	var session Session
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	injector := &delayedNativeCallbackInjector{engine: server.engine, access: access, store: server.store, sessionID: session.ID, pageEntry: entry, chromeTab: tab}
	server.injector = injector
	t.Cleanup(injector.callbacks.Wait)

	action := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/actions", ActionRequest{Command: "returnToPage"}, "test-secret")
	if action.Code != http.StatusOK {
		t.Fatalf("action status=%d body=%s", action.Code, action.Body.String())
	}
	var result ActionResult
	if err := json.Unmarshal(action.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if injector.calls != 1 || !result.State.WebContentFocused || result.State.Focus == nil || result.State.Focus.ID != entry.String() {
		t.Fatalf("delayed callback replayed physical F6 and lost native page focus: injections=%d state=%#v", injector.calls, result.State)
	}
	var focused, started, settled int
	for _, event := range result.Events {
		if event.CausalCommand != "returnToPage" || event.Sequence <= result.BeforeSequence {
			t.Fatalf("event escaped the physical action boundary: %#v", event)
		}
		switch event.Kind {
		case events.KindFocus:
			if event.Source != nil && *event.Source == entry {
				focused++
			}
		case events.KindCommandStarted:
			started++
		case events.KindCommandSettled:
			settled++
			if event.Reason != "completed" {
				t.Fatalf("delayed callback did not complete: %#v", event)
			}
		}
	}
	if focused != 1 || started != 1 || settled != 1 {
		t.Fatalf("native focus and its delayed lifecycle were not retained exactly once: focus=%d started=%d settled=%d", focused, started, settled)
	}
}

type nativeBeforeCommandInjector struct {
	store     *events.Store
	sessionID string
	command   string
	kind      events.Kind
	reason    string
}

func (i nativeBeforeCommandInjector) Press(context.Context, string) error {
	// Unconsumed keys reach Chromium before the queued key-release callback.
	i.store.Append(events.Event{Kind: i.kind, SessionID: i.sessionID, CausalCommand: i.command, Text: "Current native output", Provenance: events.ProvenanceAccessibilityEvent})
	i.store.Append(events.Event{Kind: events.KindSpeech, SessionID: i.sessionID, CausalCommand: i.command, Text: "Current native speech", Provenance: events.ProvenanceScreenReaderOutput})
	i.store.Append(events.Event{Kind: events.KindBraille, SessionID: i.sessionID, CausalCommand: i.command, Text: "Current native braille", Provenance: events.ProvenanceScreenReaderOutput})
	i.store.Append(events.Event{Kind: events.KindSpeech, SessionID: i.sessionID, CausalCommand: "nextHeading", Text: "Unrelated output"})
	i.store.Append(events.Event{Kind: events.KindFocus, SessionID: i.sessionID, CausalCommand: "nextHeading", Text: "Unrelated focus"})
	i.store.Append(events.Event{Kind: events.KindCommandStarted, SessionID: i.sessionID, CausalCommand: i.command})
	i.store.Append(events.Event{Kind: events.KindCommandSettled, SessionID: i.sessionID, CausalCommand: i.command, Reason: i.reason})
	return nil
}

func TestPhysicalActionRetainsNativeOutputBeforeQueuedCommandStart(t *testing.T) {
	for _, test := range []struct {
		name    string
		command string
		kind    events.Kind
		reason  string
		code    string
	}{
		{name: "native activation output", command: "activate", kind: events.KindLiveRegion, reason: "completed"},
		{name: "native focus before callback", command: "nextFocusable", kind: events.KindFocus, reason: "completed"},
		{name: "missing current native focus", command: "nextFocusable", kind: events.KindLiveRegion, reason: "completed", code: "focus_not_observed"},
		{name: "failed callback with native output", command: "activate", kind: events.KindLiveRegion, reason: "context deadline exceeded", code: "command_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := testServer(t)
			server.cfg.ActionTimeout = 50 * time.Millisecond
			created := request(t, server, http.MethodPost, "/v2/sessions", CreateSessionRequest{TestID: "native-before-command"}, "test-secret")
			var session Session
			if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
				t.Fatal(err)
			}
			server.store.Append(events.Event{Kind: events.KindSpeech, SessionID: session.ID, CausalCommand: test.command, Text: "Previous action output"})
			server.store.Append(events.Event{Kind: events.KindFocus, SessionID: session.ID, CausalCommand: test.command, Text: "Previous action focus"})
			server.injector = nativeBeforeCommandInjector{store: server.store, sessionID: session.ID, command: test.command, kind: test.kind, reason: test.reason}
			action := request(t, server, http.MethodPost, "/v2/sessions/"+session.ID+"/actions", ActionRequest{Command: test.command}, "test-secret")
			if test.code != "" {
				var result struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(action.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if action.Code != http.StatusGatewayTimeout || result.Error.Code != test.code {
					t.Fatalf("native output bypassed its required gate: status=%d body=%s", action.Code, action.Body.String())
				}
				return
			}
			if action.Code != http.StatusOK {
				t.Fatalf("action status=%d body=%s", action.Code, action.Body.String())
			}
			var result ActionResult
			if err := json.Unmarshal(action.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Events) != 5 || result.Events[0].Kind != test.kind || result.Events[1].Text != "Current native speech" || result.Events[2].Kind != events.KindBraille || result.Events[3].Kind != events.KindCommandStarted || result.Events[4].Kind != events.KindCommandSettled {
				t.Fatalf("native output was lost or unrelated output leaked: %#v", result.Events)
			}
			for _, event := range result.Events {
				if event.CausalCommand != test.command || event.Sequence <= result.BeforeSequence {
					t.Fatalf("event escaped its action boundary: %#v", event)
				}
			}
		})
	}
}
