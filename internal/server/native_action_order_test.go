package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/openhoo/hoovda/internal/events"
)

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
