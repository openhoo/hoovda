package engine

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/openhoo/hoovda/internal/braille"
	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
	"github.com/openhoo/hoovda/internal/profile"
	"github.com/openhoo/hoovda/internal/synth"
)

type fakeAccess struct {
	graph  *model.Graph
	events chan NativeEvent
}

func (f *fakeAccess) BrowserGraph(context.Context, string) (*model.Graph, error) { return f.graph, nil }
func (f *fakeAccess) ReadNode(_ context.Context, id model.ObjectID) (*model.Node, error) {
	return f.graph.Nodes[id], nil
}
func (f *fakeAccess) DoDefaultAction(context.Context, model.ObjectID) error { return nil }
func (f *fakeAccess) Events() <-chan NativeEvent                            { return f.events }

func fixtureEngine(t *testing.T) (*Engine, *events.Store) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	heading := model.ObjectID{Bus: "app", Path: "/heading"}
	button := model.ObjectID{Bus: "app", Path: "/button"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:    {ID: root, Role: "document web", Name: "Example", Children: []model.ObjectID{heading, button}, States: map[string]bool{"enabled": true}},
		heading: {ID: heading, Parent: root, Role: "heading", Name: "Checkout", HeadingLevel: 1, States: map[string]bool{"enabled": true}},
		button:  {ID: button, Parent: root, Role: "push button", Name: "Pay", States: map[string]bool{"enabled": true, "focusable": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	store := events.NewStore(100)
	presenter, _ := profile.NewPresenter("en-US")
	access := &fakeAccess{graph: graph, events: make(chan NativeEvent, 10)}
	engine := New(access, store, presenter, braille.Passthrough{}, synth.Silence{SampleRate: 22050}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Locale: "en-US", KeyboardLayout: "desktop", SynthRequest: synth.Request{Rate: 175, Pitch: 50, Volume: 100}})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := engine.BeginSession("test"); err != nil {
		t.Fatal(err)
	}
	return engine, store
}

func TestQuickNavigationProducesSpeechAndBraille(t *testing.T) {
	engine, store := fixtureEngine(t)
	if err := engine.ExecuteDirect(context.Background(), "nextHeading"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	got, _, err := store.Snapshot(0, "test")
	if err != nil {
		t.Fatal(err)
	}
	foundSpeech, foundBraille := false, false
	for _, event := range got {
		if event.Kind == events.KindSpeech && event.Text == "Checkout heading level 1" {
			foundSpeech = true
		}
		if event.Kind == events.KindBraille {
			foundBraille = true
		}
	}
	if !foundSpeech || !foundBraille {
		t.Fatalf("events = %#v", got)
	}
}

func TestToggleFocusMode(t *testing.T) {
	engine, _ := fixtureEngine(t)
	if err := engine.ExecuteDirect(context.Background(), "toggleFocusMode"); err != nil {
		t.Fatal(err)
	}
	if engine.State().Cursor.Mode != "focus" {
		t.Fatalf("state = %#v", engine.State())
	}
}

func TestRemapCursorPreservesDocumentAcrossObjectIDChurn(t *testing.T) {
	oldApp := model.ObjectID{Bus: "app", Path: "/old-app"}
	oldStale := model.ObjectID{Bus: "app", Path: "/old-stale"}
	oldDocument := model.ObjectID{Bus: "app", Path: "/old-document"}
	oldButton := model.ObjectID{Bus: "app", Path: "/old-button"}
	newApp := model.ObjectID{Bus: "app", Path: "/new-app"}
	newStale := model.ObjectID{Bus: "app", Path: "/new-stale"}
	newDocument := model.ObjectID{Bus: "app", Path: "/new-document"}
	newButton := model.ObjectID{Bus: "app", Path: "/new-button"}
	oldGraph, err := model.NewGraph(oldApp, map[model.ObjectID]*model.Node{
		oldApp:      {ID: oldApp, Role: "application", Children: []model.ObjectID{oldStale, oldDocument}},
		oldStale:    {ID: oldStale, Parent: oldApp, Role: "document web", Name: "Runtime"},
		oldDocument: {ID: oldDocument, Parent: oldApp, Role: "document web", Name: "Checkout", Children: []model.ObjectID{oldButton}, States: map[string]bool{"focused": true, "showing": true}},
		oldButton:   {ID: oldButton, Parent: oldDocument, Role: "push button", Name: "Continue", Attributes: map[string]string{"tag": "button"}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	newGraph, err := model.NewGraph(newApp, map[model.ObjectID]*model.Node{
		newApp:      {ID: newApp, Role: "application", Children: []model.ObjectID{newStale, newDocument}},
		newStale:    {ID: newStale, Parent: newApp, Role: "document web", Name: "Runtime", States: map[string]bool{"focused": true, "showing": true}},
		newDocument: {ID: newDocument, Parent: newApp, Role: "document web", Name: "Checkout", Children: []model.ObjectID{newButton}, States: map[string]bool{"focused": true, "showing": true}},
		newButton:   {ID: newButton, Parent: newDocument, Role: "push button", Name: "Continue", Attributes: map[string]string{"tag": "button"}},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := remapCursor(oldGraph, newGraph, oldButton); got != newButton {
		t.Fatalf("remapped cursor = %#v, want %#v", got, newButton)
	}
	if got := initialCursor(newGraph); got != newDocument {
		t.Fatalf("initial cursor = %#v, want newest active document %#v", got, newDocument)
	}
}

func TestSayAllProducesOneCompleteAudioStream(t *testing.T) {
	engine, store := fixtureEngine(t)
	if err := engine.ExecuteDirect(context.Background(), "sayAll"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := engine.WaitForSynthesis(ctx); err != nil {
		t.Fatal(err)
	}
	got, _, err := store.Snapshot(0, "test")
	if err != nil {
		t.Fatal(err)
	}
	speech, audio := 0, 0
	for _, event := range got {
		if event.Kind == events.KindSpeech && event.CausalCommand == "sayAll" {
			speech++
		}
		if event.Kind == events.KindAudio && event.CausalCommand == "sayAll" {
			audio++
		}
	}
	if speech != 3 || audio != 1 {
		t.Fatalf("speech=%d audio=%d events=%#v", speech, audio, got)
	}
}

func TestDocumentFocusRefreshesNavigationGraph(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	oldHeading := model.ObjectID{Bus: "app", Path: "/old-heading"}
	newHeading := model.ObjectID{Bus: "app", Path: "/new-heading"}
	oldGraph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:       {ID: root, Role: "document web", Name: "Bootstrap", Children: []model.ObjectID{oldHeading}, States: map[string]bool{"enabled": true}},
		oldHeading: {ID: oldHeading, Parent: root, Role: "heading", Name: "Runtime ready", HeadingLevel: 1, States: map[string]bool{"enabled": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	newGraph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:       {ID: root, Role: "document web", Name: "Checkout", Children: []model.ObjectID{newHeading}, States: map[string]bool{"enabled": true}},
		newHeading: {ID: newHeading, Parent: root, Role: "heading", Name: "Checkout", HeadingLevel: 1, States: map[string]bool{"enabled": true}},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	access := &fakeAccess{graph: oldGraph, events: make(chan NativeEvent, 1)}
	store := events.NewStore(100)
	presenter, _ := profile.NewPresenter("en-US")
	engine := New(access, store, presenter, braille.Passthrough{}, synth.Silence{SampleRate: 22050}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Locale: "en-US", KeyboardLayout: "desktop"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := engine.BeginSession("test"); err != nil {
		t.Fatal(err)
	}
	access.graph = newGraph
	access.events <- NativeEvent{Name: "org.a11y.atspi.Event.Focus.Focus", Source: root}
	deadline := time.Now().Add(time.Second)
	for !engine.State().WebContentFocused && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !engine.State().WebContentFocused {
		t.Fatalf("state = %#v", engine.State())
	}
	if err := engine.ExecuteDirect(context.Background(), "nextHeading"); err != nil {
		t.Fatal(err)
	}
	if engine.State().GraphRevision != 2 {
		t.Fatalf("state = %#v", engine.State())
	}
	got, _, err := store.Snapshot(0, "test")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range got {
		if event.Kind == events.KindSpeech && event.CausalCommand == "nextHeading" && event.Text == "Checkout heading level 1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %#v", got)
	}
}

func TestChromiumTextChangedSpeaksOnlyLiveRegion(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	ordinary := model.ObjectID{Bus: "app", Path: "/ordinary"}
	status := model.ObjectID{Bus: "app", Path: "/status"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:     {ID: root, Role: "document web", Children: []model.ObjectID{ordinary, status}},
		ordinary: {ID: ordinary, Parent: root, Role: "paragraph", Text: "Receipt timestamp updated"},
		status:   {ID: status, Parent: root, Role: "statusbar", Text: "Order review ready"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	access := &fakeAccess{graph: graph, events: make(chan NativeEvent, 2)}
	store := events.NewStore(100)
	presenter, _ := profile.NewPresenter("en-US")
	engine := New(access, store, presenter, braille.Passthrough{}, synth.Silence{SampleRate: 22050}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Locale: "en-US", KeyboardLayout: "desktop"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := engine.BeginSession("test"); err != nil {
		t.Fatal(err)
	}
	access.events <- NativeEvent{Name: "org.a11y.atspi.Event.Object.TextChanged", Source: ordinary, Detail: "insert", Value: "Receipt timestamp updated"}
	access.events <- NativeEvent{Name: "org.a11y.atspi.Event.Object.TextChanged", Source: status, Detail: "insert", Value: "Order review ready"}
	deadline := time.Now().Add(time.Second)
	for store.Cursor() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	got, _, err := store.Snapshot(0, "test")
	if err != nil {
		t.Fatal(err)
	}
	foundLive, foundSpeech := false, false
	for _, event := range got {
		if event.Text == "Receipt timestamp updated" {
			t.Fatalf("ordinary update was emitted: %#v", got)
		}
		if event.Kind == events.KindLiveRegion && event.Text == "Order review ready" && event.Priority == "polite" {
			foundLive = true
		}
		if event.Kind == events.KindSpeech && event.Text == "Order review ready" {
			foundSpeech = true
		}
	}
	if !foundLive || !foundSpeech {
		t.Fatalf("events = %#v", got)
	}
}
