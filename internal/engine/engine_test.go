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

func (f *fakeAccess) BrowserGraph(context.Context, string, model.ObjectID) (*model.Graph, error) {
	return f.graph, nil
}
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
		if event.Kind == events.KindSpeech && event.Text == "Checkout  heading  level 1" {
			foundSpeech = true
		}
		if event.Kind == events.KindBraille && event.Text == "Checkout h1" {
			foundBraille = true
		}
	}
	if !foundSpeech || !foundBraille {
		t.Fatalf("events = %#v", got)
	}
}

func TestStructuredFindAndElementsList(t *testing.T) {
	engine, store := fixtureEngine(t)
	before := store.Cursor()
	if err := engine.ExecuteDirectWithArgument(context.Background(), "find", "checkout"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "Checkout  heading  level 1" {
		t.Fatalf("find speech = %q", speech)
	}
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "findNext"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "no next text checkout" {
		t.Fatalf("find-next speech = %q", speech)
	}
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "elementsList"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "Elements list  0 links  1 headings  1 form fields  1 buttons  0 landmarks" {
		t.Fatalf("elements-list speech = %q", speech)
	}
}

func TestFindSkipsDuplicateDescendantForSameTextOccurrence(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	item := model.ObjectID{Bus: "app", Path: "/item"}
	textNode := model.ObjectID{Bus: "app", Path: "/item/text"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:     {ID: root, Role: "document web", Children: []model.ObjectID{item}},
		item:     {ID: item, Parent: root, Role: "list item", Text: "• Accessibility toolkit", Children: []model.ObjectID{textNode}},
		textNode: {ID: textNode, Parent: item, Role: "static", Text: "Accessibility toolkit"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	if err := engine.ExecuteDirectWithArgument(context.Background(), "find", "Accessibility toolkit"); err != nil {
		t.Fatal(err)
	}
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "findNext"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "no next text Accessibility toolkit" {
		t.Fatalf("find-next speech = %q", speech)
	}
}

func TestReportDetailsUsesRelationTargetOnDemand(t *testing.T) {
	engine, store := fixtureEngine(t)
	engine.mu.Lock()
	button := engine.graph.Nodes[model.ObjectID{Bus: "app", Path: "/button"}]
	button.RelationText = map[string][]string{"details": {"Payment continues after review"}}
	engine.cursor.Object = button.ID
	engine.mu.Unlock()
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "reportDetails"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "Payment continues after review" {
		t.Fatalf("details speech = %q", speech)
	}
}

func TestCharacterNavigationUsesRuneOffsetsInsideNode(t *testing.T) {
	engine, store := fixtureEngine(t)
	if err := engine.ExecuteDirect(context.Background(), "nextHeading"); err != nil {
		t.Fatal(err)
	}
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "nextCharacter"); err != nil {
		t.Fatal(err)
	}
	if state := engine.State(); state.Cursor.Offset != 1 {
		t.Fatalf("cursor after next character = %#v", state.Cursor)
	}
	if speech := speechAfter(t, store, before); speech != "h" {
		t.Fatalf("next character speech = %q", speech)
	}
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "previousCharacter"); err != nil {
		t.Fatal(err)
	}
	if state := engine.State(); state.Cursor.Offset != 0 {
		t.Fatalf("cursor after previous character = %#v", state.Cursor)
	}
	if speech := speechAfter(t, store, before); speech != "C" {
		t.Fatalf("previous character speech = %q", speech)
	}
}

func TestTextNavigationSkipsChromiumEmbeddedObjectMarker(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	heading := model.ObjectID{Bus: "app", Path: "/heading"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:    {ID: root, Role: "document web", Text: "\ufffc", Children: []model.ObjectID{heading}},
		heading: {ID: heading, Parent: root, Role: "heading", Text: "Hello", HeadingLevel: 1},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	if err := engine.ExecuteDirect(context.Background(), "documentStart"); err != nil {
		t.Fatal(err)
	}
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "nextCharacter"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "H" {
		t.Fatalf("next character speech = %q", speech)
	}
	if got := engine.State().Cursor; got.Object != heading || got.Offset != 0 {
		t.Fatalf("cursor = %#v", got)
	}
}

func TestWordAndLineNavigationRemainInsideTextNode(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	paragraph := model.ObjectID{Bus: "app", Path: "/paragraph"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:      {ID: root, Role: "document web", Name: "Example", Children: []model.ObjectID{paragraph}},
		paragraph: {ID: paragraph, Parent: root, Role: "paragraph", Text: "First line\n\nThird line", Attributes: map[string]string{"tag": "p"}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	if err := engine.ExecuteDirect(context.Background(), "nextParagraph"); err != nil {
		t.Fatal(err)
	}
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "nextWord"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "line" {
		t.Fatalf("next word speech = %q", speech)
	}
	if state := engine.State(); state.Cursor.Object != paragraph || state.Cursor.Offset != 6 {
		t.Fatalf("word cursor = %#v", state.Cursor)
	}
	if err := engine.ExecuteDirect(context.Background(), "documentStart"); err != nil {
		t.Fatal(err)
	}
	if err := engine.ExecuteDirect(context.Background(), "nextParagraph"); err != nil {
		t.Fatal(err)
	}
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "nextLine"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "blank" {
		t.Fatalf("blank line speech = %q", speech)
	}
	if state := engine.State(); state.Cursor.Object != paragraph || state.Cursor.Offset != 11 {
		t.Fatalf("line cursor = %#v", state.Cursor)
	}
}

func TestTableNavigationUsesSpansAndFirstLastCommands(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	table := model.ObjectID{Bus: "app", Path: "/table"}
	first := model.ObjectID{Bus: "app", Path: "/first"}
	wide := model.ObjectID{Bus: "app", Path: "/wide"}
	last := model.ObjectID{Bus: "app", Path: "/last"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root: {ID: root, Role: "document web", Children: []model.ObjectID{table}},
		table: {
			ID: table, Parent: root, Role: "table", Name: "Results", RowCount: 2, ColumnCount: 3,
			Children: []model.ObjectID{first, wide, last},
		},
		first: {ID: first, Parent: table, Table: table, Role: "column header", Name: "First", Row: 1, Column: 1, States: map[string]bool{"enabled": true}},
		wide:  {ID: wide, Parent: table, Table: table, Role: "table cell", Name: "Wide", Row: 2, Column: 1, ColumnSpan: 2, States: map[string]bool{"enabled": true}},
		last:  {ID: last, Parent: table, Table: table, Role: "table cell", Name: "Last", Row: 2, Column: 3, States: map[string]bool{"enabled": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	if err := engine.ExecuteDirect(context.Background(), "nextTable"); err != nil {
		t.Fatal(err)
	}
	if err := engine.ExecuteDirect(context.Background(), "lastTableRow"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().Cursor.Object; got != wide {
		t.Fatalf("last row cursor = %#v", got)
	}
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "nextTableColumn"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().Cursor.Object; got != last {
		t.Fatalf("span-aware next column cursor = %#v", got)
	}
	if speech := speechAfter(t, store, before); speech != "row 2  column 3  Last" {
		t.Fatalf("next column speech = %q", speech)
	}
	if err := engine.ExecuteDirect(context.Background(), "firstTableColumn"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().Cursor.Object; got != wide {
		t.Fatalf("first column cursor = %#v", got)
	}
}

func speechAfter(t *testing.T, store *events.Store, after uint64) string {
	t.Helper()
	items, _, err := store.Snapshot(after, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range items {
		if event.Kind == events.KindSpeech {
			return event.Text
		}
	}
	return ""
}

func engineForGraph(t *testing.T, graph *model.Graph) (*Engine, *events.Store) {
	t.Helper()
	store := events.NewStore(200)
	presenter, _ := profile.NewPresenter("en-US")
	access := &fakeAccess{graph: graph, events: make(chan NativeEvent, 10)}
	value := New(access, store, presenter, braille.Passthrough{}, synth.Silence{SampleRate: 22050}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Locale: "en-US", KeyboardLayout: "desktop"})
	if err := value.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := value.BeginSession("test"); err != nil {
		t.Fatal(err)
	}
	return value, store
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

func TestFocusPresentationUsesResolvedGraphRelations(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	entry := model.ObjectID{Bus: "app", Path: "/entry"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root: {ID: root, Role: "document web", Children: []model.ObjectID{entry}},
		entry: {
			ID: entry, Parent: root, Role: "entry", Name: "Email",
			States:       map[string]bool{"enabled": true, "focusable": true},
			RelationText: map[string][]string{"described by": {"Work address"}},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	before := store.Cursor()
	engine.handleFocus(context.Background(), entry)
	if !engine.State().WebContentFocused {
		t.Fatal("focus in web document was not recognized")
	}
	if speech := speechAfter(t, store, before); speech != "Email  entry  Work address" {
		t.Fatalf("focus speech = %q", speech)
	}
	if mode := engine.State().Cursor.Mode; mode != "focus" {
		t.Fatalf("cursor mode = %q", mode)
	}
}

func TestFocusModeReturnsToBrowseForNonWidgetFocus(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	entry := model.ObjectID{Bus: "app", Path: "/entry"}
	button := model.ObjectID{Bus: "app", Path: "/button"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:   {ID: root, Role: "document web", Children: []model.ObjectID{entry, button}},
		entry:  {ID: entry, Parent: root, Role: "entry", Name: "Email", States: map[string]bool{"focusable": true, "editable": true}},
		button: {ID: button, Parent: root, Role: "push button", Name: "Continue", States: map[string]bool{"focusable": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := engineForGraph(t, graph)
	engine.handleFocus(context.Background(), entry)
	if mode := engine.State().Cursor.Mode; mode != "focus" {
		t.Fatalf("entry mode = %q", mode)
	}
	engine.handleFocus(context.Background(), button)
	state := engine.State()
	if state.Cursor.Mode != "browse" || !state.CursorInDocument {
		t.Fatalf("button state = %#v", state)
	}
}

func TestBrowserChromeFocusPreservesVirtualBufferCursor(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	document := model.ObjectID{Bus: "app", Path: "/document"}
	tab := model.ObjectID{Bus: "app", Path: "/tab"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:     {ID: root, Role: "application", Children: []model.ObjectID{document, tab}},
		document: {ID: document, Parent: root, Role: "document web", Name: "Checkout"},
		tab:      {ID: tab, Parent: root, Role: "page tab", Name: "Checkout", States: map[string]bool{"focusable": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := engineForGraph(t, graph)
	engine.mu.Lock()
	engine.cursor.Object = document
	engine.mu.Unlock()
	engine.handleFocus(context.Background(), tab)
	state := engine.State()
	if state.Cursor.Object != document || state.Cursor.Mode != "browse" || !state.CursorInDocument || state.WebContentFocused {
		t.Fatalf("state = %#v", state)
	}
}

func TestApplicationAncestorEnablesAutomaticFocusMode(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	application := model.ObjectID{Bus: "app", Path: "/application"}
	button := model.ObjectID{Bus: "app", Path: "/button"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:        {ID: root, Role: "document web", Children: []model.ObjectID{application}},
		application: {ID: application, Parent: root, Role: "application", Children: []model.ObjectID{button}},
		button:      {ID: button, Parent: application, Role: "push button", Name: "push me", States: map[string]bool{"enabled": true, "focusable": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	before := store.Cursor()
	engine.handleFocus(context.Background(), button)
	if mode := engine.State().Cursor.Mode; mode != "focus" {
		t.Fatalf("cursor mode = %q", mode)
	}
	items, _, err := store.Snapshot(before, "test")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if item.Kind == events.KindMode && item.Mode == "focus" && item.Reason == "automaticFocusMode" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %#v", items)
	}
}

func TestFocusRefreshesGraphForUnknownObject(t *testing.T) {
	oldRoot := model.ObjectID{Bus: "app", Path: "/old-root"}
	oldGraph, err := model.NewGraph(oldRoot, map[model.ObjectID]*model.Node{
		oldRoot: {ID: oldRoot, Role: "document web", Name: "Old document"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	newRoot := model.ObjectID{Bus: "app", Path: "/new-root"}
	application := model.ObjectID{Bus: "app", Path: "/application"}
	button := model.ObjectID{Bus: "app", Path: "/button"}
	newGraph, err := model.NewGraph(newRoot, map[model.ObjectID]*model.Node{
		newRoot:     {ID: newRoot, Role: "document web", Children: []model.ObjectID{application}},
		application: {ID: application, Parent: newRoot, Role: "application", Children: []model.ObjectID{button}},
		button:      {ID: button, Parent: application, Role: "push button", Name: "push me", States: map[string]bool{"focusable": true}},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	access := &fakeAccess{graph: oldGraph, events: make(chan NativeEvent, 1)}
	store := events.NewStore(100)
	presenter, _ := profile.NewPresenter("en-US")
	engine := New(access, store, presenter, braille.Passthrough{}, synth.Silence{SampleRate: 22050}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Locale: "en-US", KeyboardLayout: "desktop"})
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	access.graph = newGraph
	engine.handleFocus(context.Background(), button)
	state := engine.State()
	if !state.WebContentFocused || state.Cursor.Mode != "focus" || state.GraphRevision != 2 {
		t.Fatalf("state = %#v", state)
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
		if event.Kind == events.KindSpeech && event.CausalCommand == "nextHeading" && event.Text == "Checkout  heading  level 1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %#v", got)
	}
}

func TestDocumentLoadInvalidatesGraphAndTargetsNewDocument(t *testing.T) {
	oldRoot := model.ObjectID{Bus: "app", Path: "/old-root"}
	oldHeading := model.ObjectID{Bus: "app", Path: "/old-heading"}
	newRoot := model.ObjectID{Bus: "app", Path: "/new-root"}
	newHeading := model.ObjectID{Bus: "app", Path: "/new-heading"}
	oldGraph, err := model.NewGraph(oldRoot, map[model.ObjectID]*model.Node{
		oldRoot:    {ID: oldRoot, Role: "document web", Name: "Bootstrap", Children: []model.ObjectID{oldHeading}},
		oldHeading: {ID: oldHeading, Parent: oldRoot, Role: "heading", Name: "Runtime ready", HeadingLevel: 1},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	newGraph, err := model.NewGraph(newRoot, map[model.ObjectID]*model.Node{
		newRoot:    {ID: newRoot, Role: "document web", Name: "Checkout", Children: []model.ObjectID{newHeading}},
		newHeading: {ID: newHeading, Parent: newRoot, Role: "heading", Name: "Checkout", HeadingLevel: 1},
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
	access.events <- NativeEvent{Name: "org.a11y.atspi.Event.Document.LoadComplete", Source: newRoot}
	deadline := time.Now().Add(time.Second)
	for engine.State().Focus != newRoot && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := engine.ExecuteDirect(context.Background(), "nextHeading"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State(); got.GraphRevision != 2 || got.Cursor.Object != newHeading {
		t.Fatalf("state = %#v", got)
	}
}

func TestGraphRefreshSurvivesFocusedControlRemoval(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	button := model.ObjectID{Bus: "app", Path: "/button"}
	heading := model.ObjectID{Bus: "app", Path: "/heading"}
	oldGraph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:   {ID: root, Role: "document web", Name: "Checkout", Children: []model.ObjectID{button}},
		button: {ID: button, Parent: root, Role: "push button", Name: "Continue", States: map[string]bool{"focusable": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	newGraph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:    {ID: root, Role: "document web", Name: "Checkout", Children: []model.ObjectID{heading}},
		heading: {ID: heading, Parent: root, Role: "heading", Name: "Complete", HeadingLevel: 1},
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
	engine.handleFocus(context.Background(), button)
	access.graph = newGraph
	access.events <- NativeEvent{Name: "org.a11y.atspi.Event.Object.ChildrenChanged", Source: root}
	deadline := time.Now().Add(time.Second)
	dirty := false
	for !dirty && time.Now().Before(deadline) {
		engine.mu.RLock()
		dirty = engine.graphDirty
		engine.mu.RUnlock()
		time.Sleep(time.Millisecond)
	}
	if !dirty {
		t.Fatal("children-changed event did not invalidate graph")
	}
	if err := engine.ExecuteDirect(context.Background(), "nextHeading"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State(); got.GraphRevision != 2 || got.Cursor.Object != heading {
		t.Fatalf("state = %#v", got)
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
