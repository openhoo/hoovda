package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openhoo/hoovda/internal/braille"
	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
	"github.com/openhoo/hoovda/internal/profile"
	"github.com/openhoo/hoovda/internal/synth"
)

type fakeAccess struct {
	graph       *model.Graph
	readNodes   map[model.ObjectID]*model.Node
	events      chan NativeEvent
	graphReads  int
	blockGraphs bool
	focused     []model.ObjectID
	activated   []model.ObjectID
	mouse       []string
}

func (f *fakeAccess) BrowserGraph(ctx context.Context, _ string, _ model.ObjectID) (*model.Graph, error) {
	f.graphReads++
	if f.blockGraphs {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.graph, nil
}
func (f *fakeAccess) ReadNode(_ context.Context, id model.ObjectID) (*model.Node, error) {
	if node := f.readNodes[id]; node != nil {
		return node, nil
	}
	return f.graph.Nodes[id], nil
}
func (f *fakeAccess) DoDefaultAction(_ context.Context, id model.ObjectID) error {
	f.activated = append(f.activated, id)
	return nil
}
func (f *fakeAccess) GrabFocus(_ context.Context, id model.ObjectID) error {
	f.focused = append(f.focused, id)
	return nil
}
func (f *fakeAccess) SetTextSelection(_ context.Context, id model.ObjectID, start, end int) error {
	if node := f.graph.Nodes[id]; node != nil {
		node.Selections = []model.TextRange{{Object: id, Start: start, End: end}}
	}
	return nil
}
func (f *fakeAccess) GenerateMouseEvent(_ context.Context, x, y int, name string) error {
	f.mouse = append(f.mouse, fmt.Sprintf("%s:%d:%d", name, x, y))
	return nil
}
func (f *fakeAccess) Events() <-chan NativeEvent { return f.events }

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

func TestUnknownFocusRefreshHonorsRequestContext(t *testing.T) {
	engine, _ := fixtureEngine(t)
	access := engine.access.(*fakeAccess)
	unknown := model.ObjectID{Bus: "app", Path: "/new-button"}
	access.readNodes = map[model.ObjectID]*model.Node{
		unknown: {ID: unknown, Role: "push button", Name: "Added", Attributes: map[string]string{"tag": "button"}, States: map[string]bool{"focusable": true}},
	}
	access.blockGraphs = true
	before := access.graphReads
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if documentFocus := engine.handleFocus(ctx, unknown); documentFocus {
		t.Fatal("button focus reported as document focus")
	}
	if access.graphReads != before+1 {
		t.Fatalf("graph reads: before=%d after=%d", before, access.graphReads)
	}
	state := engine.State()
	if state.Focus != unknown || !state.WebContentFocused {
		t.Fatalf("focus state = %#v", state)
	}
	engine.mu.RLock()
	dirty := engine.graphDirty
	engine.mu.RUnlock()
	if !dirty {
		t.Fatal("unknown focused node did not invalidate graph")
	}
}

func TestGlobalLaptopGesturesAreConsumedInFocusMode(t *testing.T) {
	engine, _ := fixtureEngine(t)
	engine.mu.Lock()
	engine.cfg.KeyboardLayout = "laptop"
	engine.cursor.Mode = "focus"
	engine.mu.Unlock()
	if !engine.ShouldConsumeGesture("capslock+f10") {
		t.Fatal("review-copy gesture leaked to Chromium in focus mode")
	}
	if !engine.ShouldConsumeGesture("capslock+space") {
		t.Fatal("NVDA mode gesture leaked to focused control")
	}
	if engine.ShouldConsumeGesture("h") {
		t.Fatal("single-letter quick navigation must reach focused control")
	}
	if engine.ShouldConsumeGesture("space") {
		t.Fatal("activation key must reach focused control")
	}
	engine.mu.Lock()
	engine.cursor.Mode = "browse"
	engine.mu.Unlock()
	if !engine.ShouldConsumeGesture("space") {
		t.Fatal("activation key must be consumed in browse mode")
	}
}

func TestDocumentStartUsesFirstReadableObjectAsVirtualCursor(t *testing.T) {
	engine, store := fixtureEngine(t)
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "documentStart"); err != nil {
		t.Fatal(err)
	}
	state := engine.State()
	if state.Cursor.Object.Path != "/heading" || state.Cursor.Offset != 0 {
		t.Fatalf("cursor = %#v", state.Cursor)
	}
	if speech := speechAfter(t, store, before); speech != "Checkout  heading  level 1" {
		t.Fatalf("document-start speech = %q", speech)
	}
}

func TestContainerQuickNavigationIncludesFirstReadableDescendant(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	landmark := model.ObjectID{Bus: "app", Path: "/main"}
	heading := model.ObjectID{Bus: "app", Path: "/main/heading"}
	list := model.ObjectID{Bus: "app", Path: "/main/list"}
	item := model.ObjectID{Bus: "app", Path: "/main/list/item"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:     {ID: root, Role: "document web", Children: []model.ObjectID{landmark}, States: map[string]bool{"enabled": true}},
		landmark: {ID: landmark, Parent: root, Role: "landmark", Attributes: map[string]string{"xml-roles": "main"}, Children: []model.ObjectID{heading, list}, States: map[string]bool{"enabled": true}},
		heading:  {ID: heading, Parent: landmark, Role: "heading", Name: "Checkout", HeadingLevel: 1, States: map[string]bool{"enabled": true}},
		list:     {ID: list, Parent: landmark, Role: "list", SetSize: 1, Children: []model.ObjectID{item}, States: map[string]bool{"enabled": true}},
		item:     {ID: item, Parent: list, Role: "list item", Text: "• First item", SetSize: 1, PositionInSet: 1, States: map[string]bool{"enabled": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "nextLandmark"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "main landmark  Checkout  heading  level 1" {
		t.Fatalf("landmark speech = %q", speech)
	}
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "nextList"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "list  with 1 item  • First item" {
		t.Fatalf("list speech = %q", speech)
	}
}

func TestNotLinkBlockUsesThirtyCharacterGapsBetweenLinks(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	link1 := model.ObjectID{Bus: "app", Path: "/link-1"}
	heading := model.ObjectID{Bus: "app", Path: "/heading"}
	paragraph1 := model.ObjectID{Bus: "app", Path: "/paragraph-1"}
	link2 := model.ObjectID{Bus: "app", Path: "/link-2"}
	math := model.ObjectID{Bus: "app", Path: "/math"}
	paragraph2 := model.ObjectID{Bus: "app", Path: "/paragraph-2"}
	link3 := model.ObjectID{Bus: "app", Path: "/link-3"}
	after := model.ObjectID{Bus: "app", Path: "/after"}
	children := []model.ObjectID{link1, heading, paragraph1, link2, math, paragraph2, link3, after}
	nodes := map[model.ObjectID]*model.Node{
		root:       {ID: root, Role: "document web", Children: children, States: map[string]bool{"enabled": true}},
		link1:      {ID: link1, Parent: root, Role: "link", Name: "First", States: map[string]bool{"enabled": true}},
		heading:    {ID: heading, Parent: root, Role: "heading", Name: "Checkout", HeadingLevel: 1, States: map[string]bool{"enabled": true}},
		paragraph1: {ID: paragraph1, Parent: root, Role: "paragraph", Text: "This gap contains considerably more than thirty characters.", States: map[string]bool{"enabled": true}},
		link2:      {ID: link2, Parent: root, Role: "link", Name: "Reference", States: map[string]bool{"enabled": true}},
		math:       {ID: math, Parent: root, Role: "math", Name: "Parity math", States: map[string]bool{"enabled": true}},
		paragraph2: {ID: paragraph2, Parent: root, Role: "paragraph", Text: "This second gap also contains more than thirty characters.", States: map[string]bool{"enabled": true}},
		link3:      {ID: link3, Parent: root, Role: "link", Name: "Last", States: map[string]bool{"enabled": true}},
		after:      {ID: after, Parent: root, Role: "paragraph", Text: "After", States: map[string]bool{"enabled": true}},
	}
	graph, err := model.NewGraph(root, nodes, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "nextNotLinkBlock"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); !strings.Contains(speech, "Checkout") {
		t.Fatalf("forward non-link block = %q", speech)
	}
	engine.mu.Lock()
	engine.cursor.Object = after
	engine.mu.Unlock()
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "previousNotLinkBlock"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); !strings.Contains(speech, "Parity math") {
		t.Fatalf("backward non-link block = %q", speech)
	}
}

func TestVirtualLineParagraphAndDocumentBoundaryNavigation(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	labels := []string{
		"Parity visited link", "Navigation link one", "Navigation link two",
		"Navigation link three", "Navigation link four", "Navigation link five", "Navigation link six",
	}
	children := make([]model.ObjectID, 0, len(labels)+2)
	nodes := map[model.ObjectID]*model.Node{
		root: {ID: root, Role: "document web", States: map[string]bool{"enabled": true}},
	}
	for index, label := range labels {
		id := model.ObjectID{Bus: "app", Path: fmt.Sprintf("/link-%d", index)}
		children = append(children, id)
		nodes[id] = &model.Node{ID: id, Parent: root, Role: "link", Name: label, Attributes: map[string]string{"display": "inline"}, States: map[string]bool{"enabled": true}}
	}
	heading1 := model.ObjectID{Bus: "app", Path: "/heading-1"}
	heading2 := model.ObjectID{Bus: "app", Path: "/heading-2"}
	children = append(children, heading1, heading2)
	nodes[heading1] = &model.Node{ID: heading1, Parent: root, Role: "heading", Name: "Checkout", HeadingLevel: 1, Attributes: map[string]string{"display": "block"}, States: map[string]bool{"enabled": true}}
	nodes[heading2] = &model.Node{ID: heading2, Parent: root, Role: "heading", Name: "Shipping", HeadingLevel: 2, Attributes: map[string]string{"display": "block"}, States: map[string]bool{"enabled": true}}
	nodes[root].Children = children
	graph, err := model.NewGraph(root, nodes, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	if err := engine.ExecuteDirect(context.Background(), "documentStart"); err != nil {
		t.Fatal(err)
	}
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "nextLine"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); !strings.Contains(speech, "Navigation link five") || !strings.Contains(speech, "Navigation link six") {
		t.Fatalf("next virtual line = %q", speech)
	}
	if err := engine.ExecuteDirect(context.Background(), "documentStart"); err != nil {
		t.Fatal(err)
	}
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "nextParagraphText"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); !strings.Contains(speech, "Checkout") {
		t.Fatalf("next paragraph = %q", speech)
	}
	if err := engine.ExecuteDirect(context.Background(), "nextParagraphText"); err != nil {
		t.Fatal(err)
	}
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "previousParagraphText"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); !strings.Contains(speech, "Checkout") {
		t.Fatalf("previous paragraph = %q", speech)
	}
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "documentEnd"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "blank" {
		t.Fatalf("document end = %q", speech)
	}
	evidence, _, err := store.Snapshot(before, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range evidence {
		if event.Kind == events.KindBraille && event.CausalCommand == "documentEnd" {
			t.Fatalf("speech-only boundary emitted empty braille event: %#v", event)
		}
	}
}

func TestNativeFocusCausalityOnlyUsesFocusChangingCommands(t *testing.T) {
	engine, store := fixtureEngine(t)
	if err := engine.BeginAction("test", "documentStart"); err != nil {
		t.Fatal(err)
	}
	engine.handleFocus(context.Background(), model.ObjectID{Bus: "app", Path: "/heading"})
	engine.EndAction("test", "documentStart")
	if err := engine.BeginAction("test", "nextFocusable"); err != nil {
		t.Fatal(err)
	}
	engine.handleFocus(context.Background(), model.ObjectID{Bus: "app", Path: "/button"})
	engine.EndAction("test", "nextFocusable")

	got, _, err := store.Snapshot(0, "test")
	if err != nil {
		t.Fatal(err)
	}
	var focusEvents []events.Event
	for _, event := range got {
		if event.Kind == events.KindFocus {
			focusEvents = append(focusEvents, event)
		}
	}
	if len(focusEvents) != 2 || focusEvents[0].CausalCommand != "" || focusEvents[1].CausalCommand != "nextFocusable" {
		t.Fatalf("focus causality = %#v", focusEvents)
	}
}

func TestFocusedInteractiveStateChangeIsPresentedWithActionCausality(t *testing.T) {
	engine, store := fixtureEngine(t)
	button := model.ObjectID{Bus: "app", Path: "/button"}
	node := engine.access.(*fakeAccess).graph.Nodes[button]
	node.Role = "check box"
	engine.handleFocus(context.Background(), button)
	before := store.Cursor()
	if err := engine.BeginAction("test", "activateWithSpace"); err != nil {
		t.Fatal(err)
	}
	engine.handleStateChange(context.Background(), NativeEvent{
		Name: "org.a11y.atspi.Event.Object.StateChanged", Source: button,
		Detail: "checked", Detail1: 1,
	})
	engine.EndAction("test", "activateWithSpace")
	got, _, err := store.Snapshot(before, "test")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range got {
		if event.Kind == events.KindSpeech && strings.Contains(event.Text, "checked") {
			found = event.CausalCommand == "activateWithSpace" && event.Reason == "nativeStateChange"
		}
	}
	if !found {
		t.Fatalf("state-change presentation = %#v", got)
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

func TestEscapeReturnsToBrowsePositionPresentation(t *testing.T) {
	engine, store := fixtureEngine(t)
	if err := engine.ExecuteDirect(context.Background(), "documentStart"); err != nil {
		t.Fatal(err)
	}
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "escape"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); !strings.Contains(speech, "Checkout") || strings.Contains(speech, "browse mode") {
		t.Fatalf("escape speech = %q", speech)
	}
}

func TestBrowseDocumentContainerMovement(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	list := model.ObjectID{Bus: "app", Path: "/list"}
	item := model.ObjectID{Bus: "app", Path: "/list/item"}
	after := model.ObjectID{Bus: "app", Path: "/after"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:  {ID: root, Role: "document web", Children: []model.ObjectID{list, after}, States: map[string]bool{"enabled": true}},
		list:  {ID: list, Parent: root, Role: "list", Name: "Parity list", Children: []model.ObjectID{item}, States: map[string]bool{"enabled": true}},
		item:  {ID: item, Parent: list, Role: "list item", Name: "First item", States: map[string]bool{"enabled": true}},
		after: {ID: after, Parent: root, Role: "paragraph", Text: "After list", States: map[string]bool{"enabled": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	engine.mu.Lock()
	engine.cursor.Object = item
	engine.mu.Unlock()
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "moveToContainerStart"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().Cursor.Object; got != list {
		t.Fatalf("container start cursor = %#v", got)
	}
	if speech := speechAfter(t, store, before); speech != "Parity list  list" {
		t.Fatalf("container start speech = %q", speech)
	}
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "movePastContainerEnd"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().Cursor.Object; got != after {
		t.Fatalf("container end cursor = %#v", got)
	}
	if speech := speechAfter(t, store, before); speech != "After list  paragraph" {
		t.Fatalf("container end speech = %q", speech)
	}
}

func TestBrowseDocumentRefreshAndNativeSelection(t *testing.T) {
	engine, store := fixtureEngine(t)
	engine.mu.RLock()
	access := engine.access.(*fakeAccess)
	root := engine.graph.Root
	engine.mu.RUnlock()
	refreshed, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root: {ID: root, Role: "document web", Name: "Refreshed example"},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	access.graph = refreshed
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "refreshBrowseDocument"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().GraphRevision; got != 2 {
		t.Fatalf("graph revision = %d", got)
	}
	if speech := speechAfter(t, store, before); speech != "Refreshed" {
		t.Fatalf("refresh speech = %q", speech)
	}
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "toggleNativeSelection"); err != nil {
		t.Fatal(err)
	}
	if !engine.State().NativeSelectionMode {
		t.Fatal("native selection mode remained disabled")
	}
	if speech := speechAfter(t, store, before); speech != "Native app selection mode enabled" {
		t.Fatalf("native selection speech = %q", speech)
	}
}

func TestObjectNavigationMaintainsIndependentNavigatorAndReviewPositions(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	group := model.ObjectID{Bus: "app", Path: "/group"}
	layout := model.ObjectID{Bus: "app", Path: "/group/layout"}
	previous := model.ObjectID{Bus: "app", Path: "/group/previous"}
	current := model.ObjectID{Bus: "app", Path: "/group/current"}
	next := model.ObjectID{Bus: "app", Path: "/group/next"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:     {ID: root, Role: "document web", Name: "Object fixture", Children: []model.ObjectID{group}},
		group:    {ID: group, Parent: root, Role: "group", Name: "Object navigation group", Children: []model.ObjectID{layout}},
		layout:   {ID: layout, Parent: group, Role: "filler", Children: []model.ObjectID{previous, current, next}},
		previous: {ID: previous, Parent: layout, Role: "push button", Name: "Object previous", States: map[string]bool{"enabled": true, "focusable": true}},
		current:  {ID: current, Parent: layout, Role: "push button", Name: "Object current", States: map[string]bool{"enabled": true, "focusable": true}},
		next:     {ID: next, Parent: layout, Role: "push button", Name: "Object next", States: map[string]bool{"enabled": true, "focusable": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	engine.mu.Lock()
	engine.cursor.Object = root
	engine.navigator = current
	engine.review = model.Cursor{Object: current, Offset: 0, Mode: "object"}
	engine.mu.Unlock()

	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "reportCurrentObject"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "Object current  button" {
		t.Fatalf("current object speech = %q", speech)
	}
	if got := engine.State().Cursor.Object; got != root {
		t.Fatalf("browse cursor changed to %#v", got)
	}

	if err := engine.ExecuteDirect(context.Background(), "moveToContainingObject"); err != nil {
		t.Fatal(err)
	}
	if state := engine.State(); state.Navigator != group || state.Review.Object != group {
		t.Fatalf("parent state = %#v", state)
	}
	if err := engine.ExecuteDirect(context.Background(), "moveToFirstContainedObject"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().Navigator; got != previous {
		t.Fatalf("first simple child = %#v", got)
	}
	if err := engine.ExecuteDirect(context.Background(), "moveToNextObject"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().Navigator; got != current {
		t.Fatalf("next simple sibling = %#v", got)
	}
	if err := engine.ExecuteDirect(context.Background(), "moveToPreviousObject"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().Navigator; got != previous {
		t.Fatalf("previous simple sibling = %#v", got)
	}

	engine.mu.Lock()
	engine.navigator = group
	engine.review.Object = group
	engine.mu.Unlock()
	if err := engine.ExecuteDirect(context.Background(), "moveToNextObjectFlat"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().Navigator; got != previous {
		t.Fatalf("next flat object = %#v", got)
	}
	engine.mu.Lock()
	engine.navigator = current
	engine.review.Object = current
	engine.mu.Unlock()
	if err := engine.ExecuteDirect(context.Background(), "moveToPreviousObjectFlat"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().Navigator; got != previous {
		t.Fatalf("previous flat object = %#v", got)
	}
}

func TestObjectNavigationActivationFocusAndNativeFocusSynchronization(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	button := model.ObjectID{Bus: "app", Path: "/button"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:   {ID: root, Role: "document web", Name: "Object fixture", Children: []model.ObjectID{button}},
		button: {ID: button, Parent: root, Role: "push button", Name: "Object current", Bounds: model.Rect{X: 11, Y: 22, Width: 33, Height: 44}, States: map[string]bool{"enabled": true, "focusable": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	access := engine.access.(*fakeAccess)
	engine.handleFocus(context.Background(), button)
	if state := engine.State(); state.Navigator != button || state.Review.Object != button {
		t.Fatalf("native focus state = %#v", state)
	}

	if err := engine.ExecuteDirect(context.Background(), "activateNavigatorObject"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(access.activated, []model.ObjectID{button}) {
		t.Fatalf("activated = %#v", access.activated)
	}
	if err := engine.ExecuteDirect(context.Background(), "moveFocusToReviewPosition"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(access.focused, []model.ObjectID{button}) {
		t.Fatalf("focused = %#v", access.focused)
	}
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "reportReviewLocation"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "left 11, top 22, width 33, height 44" {
		t.Fatalf("review location speech = %q", speech)
	}
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "moveToFocusObject"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "Object current  button" {
		t.Fatalf("focus object speech = %q", speech)
	}
}

func TestReviewCursorNavigatesLinesWordsCharactersAndSelections(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	entry := model.ObjectID{Bus: "app", Path: "/review"}
	text := "Alpha beta\nGamma delta\nOmega last"
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root: {ID: root, Role: "document web", Name: "Review fixture", Children: []model.ObjectID{entry}},
		entry: {
			ID: entry, Parent: root, Role: "entry", Name: "Review text", Text: text,
			CaretOffset: 11, Selections: []model.TextRange{{Object: entry, Start: 11, End: 16}},
			States: map[string]bool{"enabled": true, "editable": true, "focusable": true},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	engine.mu.Lock()
	engine.navigator = entry
	engine.review = model.Cursor{Object: entry, Offset: 11, Mode: "object"}
	engine.mu.Unlock()

	tests := []struct {
		command string
		offset  int
		want    string
	}{
		{"reviewCurrentLine", 11, "Gamma delta"},
		{"reviewPreviousLine", 11, "Alpha beta"},
		{"reviewNextLine", 11, "Omega last"},
		{"reviewTopLine", 22, "Alpha beta"},
		{"reviewBottomLine", 0, "Omega last"},
		{"reviewCurrentWord", 11, "Gamma"},
		{"reviewPreviousWord", 11, "beta"},
		{"reviewNextWord", 11, "delta"},
		{"reviewLineStart", 17, "G"},
		{"reviewLineEnd", 11, "a"},
		{"reviewCurrentCharacter", 11, "G"},
		{"reviewPreviousCharacter", 12, "G"},
		{"reviewNextCharacter", 11, "a"},
		{"reviewSelectionStart", 0, "G"},
		{"reviewSelectionEnd", 0, "a"},
	}
	for _, item := range tests {
		engine.mu.Lock()
		engine.review.Object, engine.review.Offset = entry, item.offset
		engine.mu.Unlock()
		before := store.Cursor()
		if err := engine.ExecuteDirect(context.Background(), item.command); err != nil {
			t.Fatalf("%s: %v", item.command, err)
		}
		if speech := speechAfter(t, store, before); speech != item.want {
			t.Fatalf("%s speech = %q, want %q", item.command, speech, item.want)
		}
	}

	engine.mu.Lock()
	engine.review.Offset = 0
	engine.mu.Unlock()
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "reviewPreviousCharacter"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "Left  A" {
		t.Fatalf("left boundary speech = %q", speech)
	}
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "reviewPreviousPage"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "Movement by page not supported" {
		t.Fatalf("page boundary speech = %q", speech)
	}
}

func TestReviewCursorCopyFormattingSayAllAndModes(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	entry := model.ObjectID{Bus: "app", Path: "/review"}
	text := "Alpha beta\nGamma delta"
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root: {ID: root, Role: "document web", Name: "Review fixture", Children: []model.ObjectID{entry}},
		entry: {
			ID: entry, Parent: root, Role: "entry", Name: "Review text", Text: text,
			TextAttributeRuns: []model.TextAttributeRun{{Start: 0, End: len([]rune(text)), Attributes: map[string]string{"font-size": "24px", "font-weight": "700"}}},
			States:            map[string]bool{"enabled": true, "editable": true, "focusable": true},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	access := engine.access.(*fakeAccess)
	engine.mu.Lock()
	engine.navigator = entry
	engine.review = model.Cursor{Object: entry, Offset: 0, Mode: "object"}
	engine.mu.Unlock()

	if err := engine.ExecuteDirect(context.Background(), "setReviewCopyStart"); err != nil {
		t.Fatal(err)
	}
	engine.mu.Lock()
	engine.review.Offset = 4
	engine.mu.Unlock()
	if err := engine.ExecuteDirect(context.Background(), "copyToReviewPosition"); err != nil {
		t.Fatal(err)
	}
	if got := access.graph.Nodes[entry].Selections; !slices.Equal(got, []model.TextRange{{Object: entry, Start: 0, End: 5}}) {
		t.Fatalf("selection = %#v", got)
	}
	state := engine.State()
	if state.ReviewCopyStart == nil || state.ReviewSelection == nil || state.ReviewSelection.End != 5 {
		t.Fatalf("review copy state = %#v", state)
	}
	engine.mu.Lock()
	engine.review.Offset = 8
	engine.mu.Unlock()
	if err := engine.ExecuteDirect(context.Background(), "moveToReviewCopyStart"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().Review.Offset; got != 0 {
		t.Fatalf("review copy start offset = %d", got)
	}

	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "reportReviewFormatting"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "24 px bold" {
		t.Fatalf("review formatting speech = %q", speech)
	}
	engine.mu.Lock()
	engine.review.Offset = 11
	engine.mu.Unlock()
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "sayAllReview"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "Gamma delta" {
		t.Fatalf("say-all review speech = %q", speech)
	}

	engine.mu.Lock()
	engine.review.Mode = "object"
	engine.mu.Unlock()
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "nextReviewMode"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "Document review" {
		t.Fatalf("next review mode speech = %q", speech)
	}
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "previousReviewMode"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "Object review" {
		t.Fatalf("previous review mode speech = %q", speech)
	}
}

func TestMouseCommandsMoveClickLockAndResolveNavigatorObject(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	previous := model.ObjectID{Bus: "app", Path: "/previous"}
	target := model.ObjectID{Bus: "app", Path: "/target"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:     {ID: root, Role: "document web", Name: "Mouse fixture", Bounds: model.Rect{X: 0, Y: 0, Width: 800, Height: 600}, Children: []model.ObjectID{previous, target}},
		previous: {ID: previous, Parent: root, Role: "push button", Name: "Before mouse", Bounds: model.Rect{X: 200, Y: 20, Width: 100, Height: 40}},
		target:   {ID: target, Parent: root, Role: "push button", Name: "Mouse action", Bounds: model.Rect{X: 10, Y: 20, Width: 100, Height: 40}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	access := engine.access.(*fakeAccess)
	engine.mu.Lock()
	engine.navigator = target
	engine.review = model.Cursor{Object: target, Mode: "object"}
	engine.mu.Unlock()

	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "moveMouseToNavigatorObject"); err != nil {
		t.Fatal(err)
	}
	if state := engine.State(); !state.MousePositionKnown || state.MouseX != 60 || state.MouseY != 40 {
		t.Fatalf("mouse state = %#v", state)
	}
	if speech := speechAfter(t, store, before); speech != "" {
		t.Fatalf("mouse move speech = %q", speech)
	}
	for _, command := range []string{"leftMouseClick", "rightMouseClick", "leftMouseLock", "rightMouseLock", "leftMouseLock", "rightMouseLock"} {
		if err := engine.ExecuteDirect(context.Background(), command); err != nil {
			t.Fatalf("%s: %v", command, err)
		}
	}
	wantEvents := []string{"abs:60:40", "b1c:60:40", "b3c:60:40", "b1p:60:40", "b3p:60:40", "b1r:60:40", "b3r:60:40"}
	if !slices.Equal(access.mouse, wantEvents) {
		t.Fatalf("mouse events = %#v", access.mouse)
	}
	if state := engine.State(); state.LeftMouseLocked || state.RightMouseLocked {
		t.Fatalf("mouse locks remained set: %#v", state)
	}

	engine.mu.Lock()
	engine.navigator = previous
	engine.review.Object = previous
	engine.mu.Unlock()
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "moveNavigatorToMouseObject"); err != nil {
		t.Fatal(err)
	}
	if state := engine.State(); state.Navigator != target || state.Review.Object != target {
		t.Fatalf("mouse navigator state = %#v", state)
	}
	if speech := speechAfter(t, store, before); !strings.Contains(speech, "Mouse action") {
		t.Fatalf("mouse navigator speech = %q", speech)
	}
}

func TestSpeechAndBrailleControlsMaintainStateAndRouteCells(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	entry := model.ObjectID{Bus: "app", Path: "/braille"}
	text := "Alpha beta\nGamma delta and a deliberately long braille line that exceeds forty cells"
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root: {ID: root, Role: "document web", Name: "Braille fixture", Children: []model.ObjectID{entry}},
		entry: {
			ID: entry, Parent: root, Role: "entry", Name: "Braille text", Text: text,
			TextAttributeRuns: []model.TextAttributeRun{{Start: 0, End: len([]rune(text)), Attributes: map[string]string{"font-size": "24px"}}},
			States:            map[string]bool{"enabled": true, "editable": true, "focusable": true},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	engine.mu.Lock()
	engine.navigator = entry
	engine.review = model.Cursor{Object: entry, Offset: 0, Mode: "object"}
	engine.mu.Unlock()

	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "cycleSpeechMode"); err != nil {
		t.Fatal(err)
	}
	if state := engine.State(); state.SpeechMode != "on-demand" {
		t.Fatalf("speech mode state = %#v", state)
	}
	if speech := speechAfter(t, store, before); speech != "Speech mode on-demand" {
		t.Fatalf("speech mode output = %q", speech)
	}
	if err := engine.ExecuteDirect(context.Background(), "pauseSpeech"); err != nil {
		t.Fatal(err)
	}
	if !engine.State().SpeechPaused {
		t.Fatal("speech did not pause")
	}
	if err := engine.ExecuteDirect(context.Background(), "stopSpeech"); err != nil {
		t.Fatal(err)
	}
	if engine.State().SpeechPaused {
		t.Fatal("stop speech did not clear pause")
	}

	if err := engine.ExecuteDirect(context.Background(), "brailleToggleTether"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().BrailleTether; got != "focus" {
		t.Fatalf("braille tether = %q", got)
	}
	engine.mu.Lock()
	engine.focus = entry
	engine.mu.Unlock()
	if err := engine.ExecuteDirect(context.Background(), "braillePanForward"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().BrailleOffset; got != 11 {
		t.Fatalf("braille forward offset = %d", got)
	}
	if err := engine.ExecuteDirect(context.Background(), "braillePanBack"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().BrailleOffset; got != 0 {
		t.Fatalf("braille back offset = %d", got)
	}
	if err := engine.ExecuteDirect(context.Background(), "brailleNextLine"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().Review.Offset; got != 11 {
		t.Fatalf("braille next line offset = %d", got)
	}
	if err := engine.ExecuteDirect(context.Background(), "braillePreviousLine"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().Review.Offset; got != 0 {
		t.Fatalf("braille previous line offset = %d", got)
	}
	if err := engine.ExecuteDirectWithArgument(context.Background(), "brailleRoute", "6"); err != nil {
		t.Fatal(err)
	}
	if state := engine.State(); state.Review.Offset != 6 || state.Cursor.Offset != 6 {
		t.Fatalf("braille route state = %#v", state)
	}
	before = store.Cursor()
	if err := engine.ExecuteDirectWithArgument(context.Background(), "brailleReportFormatting", "0"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "24 px" {
		t.Fatalf("braille formatting speech = %q", speech)
	}
}

func TestBrailleTetherCyclesWithoutSourceObject(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root: {ID: root, Role: "document web", Name: "Fixture"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := engineForGraph(t, graph)
	engine.mu.Lock()
	engine.graph = nil
	engine.focus = model.ObjectID{}
	engine.navigator = model.ObjectID{}
	engine.review = model.Cursor{Mode: "object"}
	engine.mu.Unlock()
	if err := engine.ExecuteDirect(context.Background(), "brailleToggleTether"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().BrailleTether; got != "focus" {
		t.Fatalf("braille tether = %q", got)
	}
}

func TestBraillePanAndRouteUseLogicalMultilineDisplayLines(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	entry := model.ObjectID{Bus: "app", Path: "/entry"}
	text := "Alpha beta\nGamma delta\nOmega last"
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:  {ID: root, Role: "document web", Children: []model.ObjectID{entry}},
		entry: {ID: entry, Parent: root, Role: "entry", Text: text, States: map[string]bool{"focused": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	engine.mu.Lock()
	engine.focus = entry
	engine.review = model.Cursor{Object: entry, Offset: 11, Mode: "object"}
	engine.mu.Unlock()
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "braillePanForward"); err != nil {
		t.Fatal(err)
	}
	if braille := brailleAfter(t, store, before); braille != "Omega last" {
		t.Fatalf("pan forward braille = %q", braille)
	}
	engine.mu.Lock()
	engine.brailleOffset = 0
	engine.mu.Unlock()
	before = store.Cursor()
	if err := engine.ExecuteDirectWithArgument(context.Background(), "brailleRoute", "0"); err != nil {
		t.Fatal(err)
	}
	if braille := brailleAfter(t, store, before); braille != "Alpha beta" {
		t.Fatalf("route braille = %q", braille)
	}
}

func TestExitEmbeddedObjectFocusesContainingDocument(t *testing.T) {
	outer := model.ObjectID{Bus: "app", Path: "/outer"}
	embedded := model.ObjectID{Bus: "app", Path: "/outer/plugin"}
	inner := model.ObjectID{Bus: "app", Path: "/outer/plugin/document"}
	button := model.ObjectID{Bus: "app", Path: "/outer/plugin/document/button"}
	graph, err := model.NewGraph(outer, map[model.ObjectID]*model.Node{
		outer:    {ID: outer, Role: "document web", Name: "Outer", Children: []model.ObjectID{embedded}},
		embedded: {ID: embedded, Parent: outer, Role: "embedded", Name: "Plugin", Children: []model.ObjectID{inner}},
		inner:    {ID: inner, Parent: embedded, Role: "document frame", Name: "Inner", Children: []model.ObjectID{button}},
		button:   {ID: button, Parent: inner, Role: "push button", Name: "Inside"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := engineForGraph(t, graph)
	access := engine.access.(*fakeAccess)
	engine.mu.Lock()
	engine.cursor.Object = button
	engine.mu.Unlock()
	if err := engine.ExecuteDirect(context.Background(), "exitEmbeddedObject"); err != nil {
		t.Fatal(err)
	}
	if len(access.focused) != 1 || access.focused[0] != outer {
		t.Fatalf("focused = %#v", access.focused)
	}
	if got := engine.State().Cursor.Object; got != outer {
		t.Fatalf("cursor = %#v", got)
	}
}

func TestExitEmbeddedObjectLeavesOrdinaryIframeInSharedDocument(t *testing.T) {
	outer := model.ObjectID{Bus: "app", Path: "/outer"}
	inner := model.ObjectID{Bus: "app", Path: "/outer/frame"}
	button := model.ObjectID{Bus: "app", Path: "/outer/frame/button"}
	graph, err := model.NewGraph(outer, map[model.ObjectID]*model.Node{
		outer:  {ID: outer, Role: "document web", Children: []model.ObjectID{inner}},
		inner:  {ID: inner, Parent: outer, Role: "document frame", Children: []model.ObjectID{button}},
		button: {ID: button, Parent: inner, Role: "push button", Name: "Inside"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := engineForGraph(t, graph)
	access := engine.access.(*fakeAccess)
	engine.mu.Lock()
	engine.cursor.Object = button
	engine.mu.Unlock()
	if err := engine.ExecuteDirect(context.Background(), "exitEmbeddedObject"); err != nil {
		t.Fatal(err)
	}
	if len(access.focused) != 0 || engine.State().Cursor.Object != button {
		t.Fatalf("focused = %#v, cursor = %#v", access.focused, engine.State().Cursor.Object)
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
	if speech := speechAfter(t, store, before); speech != "e" {
		t.Fatalf("next character speech = %q", speech)
	}
	if got := engine.State().Cursor; got.Object != heading || got.Offset != 1 {
		t.Fatalf("cursor = %#v", got)
	}
}

func TestWordAndLineNavigationRemainInsideTextNode(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	paragraph := model.ObjectID{Bus: "app", Path: "/paragraph"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:      {ID: root, Role: "document web", Name: "Example", Children: []model.ObjectID{paragraph}},
		paragraph: {ID: paragraph, Parent: root, Role: "paragraph", Text: "First line!\n\nThird line", Attributes: map[string]string{"tag": "p"}},
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
	if speech := speechAfter(t, store, before); speech != "line!" {
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
	if state := engine.State(); state.Cursor.Object != paragraph || state.Cursor.Offset != 12 {
		t.Fatalf("line cursor = %#v", state.Cursor)
	}
}

func TestQuickTextParagraphReportsOnlyText(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	paragraph := model.ObjectID{Bus: "app", Path: "/paragraph"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:      {ID: root, Role: "document web", Children: []model.ObjectID{paragraph}},
		paragraph: {ID: paragraph, Parent: root, Role: "paragraph", Text: "Hello, world!"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "nextParagraph"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "Hello, world!" {
		t.Fatalf("speech = %q", speech)
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
	if got := engine.State().Cursor.Object; got != first {
		t.Fatalf("table entry cursor = %#v", got)
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
	if speech := speechAfter(t, store, before); speech != "column 3  Last" {
		t.Fatalf("next column speech = %q", speech)
	}
	if err := engine.ExecuteDirect(context.Background(), "firstTableColumn"); err != nil {
		t.Fatal(err)
	}
	if got := engine.State().Cursor.Object; got != wide {
		t.Fatalf("first column cursor = %#v", got)
	}
}

func TestWebReportingCommandsUseAccessibleEvidence(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	link := model.ObjectID{Bus: "app", Path: "/link"}
	paragraph := model.ObjectID{Bus: "app", Path: "/paragraph"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root: {ID: root, Role: "document web", Name: "Parity title", Children: []model.ObjectID{link, paragraph}, States: map[string]bool{"enabled": true}},
		link: {
			ID: link, Parent: root, Role: "link", Name: "Documentation", KeyboardShortcut: "Alt+D",
			Attributes: map[string]string{"url": "https://example.test/docs"}, States: map[string]bool{"enabled": true},
		},
		paragraph: {
			ID: paragraph, Parent: root, Role: "paragraph", Text: "First line\nSecond line", Locale: "de-DE",
			TextAttributeRuns: []model.TextAttributeRun{{Start: 0, End: 22, Attributes: map[string]string{"font-weight": "700"}}},
			Selections:        []model.TextRange{{Object: paragraph, Start: 0, End: 5}},
			Bounds:            model.Rect{X: 10, Y: 20, Width: 300, Height: 40}, States: map[string]bool{"enabled": true},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	engine.mu.Lock()
	engine.cursor.Object = paragraph
	engine.cursor.Offset = 12
	engine.focus = link
	engine.mu.Unlock()
	cases := []struct {
		command string
		want    string
	}{
		{"reportTitle", "Parity title"},
		{"reportShortcutKey", "Alt+D"},
		{"reportCurrentLine", "Second line"},
		{"reportTextSelection", "First selected"},
		{"reportTextFormatting", "bold"},
		{"reportLanguage", "German (Germany) (not supported)"},
		{"reportCaretLocation", "left 10, top 20, width 300, height 40"},
	}
	for _, item := range cases {
		before := store.Cursor()
		if err := engine.ExecuteDirect(context.Background(), item.command); err != nil {
			t.Fatalf("%s: %v", item.command, err)
		}
		if speech := speechAfter(t, store, before); speech != item.want {
			t.Fatalf("%s speech = %q, want %q", item.command, speech, item.want)
		}
	}
	engine.mu.Lock()
	engine.cursor.Object = link
	engine.mu.Unlock()
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "reportLinkDestination"); err != nil {
		t.Fatal(err)
	}
	if speech := speechAfter(t, store, before); speech != "https://example.test/docs" {
		t.Fatalf("link destination = %q", speech)
	}
}

func TestTableAxisReportingReadsExpectedCellsAndCaretBehavior(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	table := model.ObjectID{Bus: "app", Path: "/table"}
	a := model.ObjectID{Bus: "app", Path: "/a"}
	b := model.ObjectID{Bus: "app", Path: "/b"}
	c := model.ObjectID{Bus: "app", Path: "/c"}
	d := model.ObjectID{Bus: "app", Path: "/d"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:  {ID: root, Role: "document web", Children: []model.ObjectID{table}},
		table: {ID: table, Parent: root, Role: "table", RowCount: 2, ColumnCount: 2, Children: []model.ObjectID{a, b, c, d}},
		a:     {ID: a, Parent: table, Table: table, Role: "column header", Name: "A", Row: 1, Column: 1, States: map[string]bool{"enabled": true}},
		b:     {ID: b, Parent: table, Table: table, Role: "column header", Name: "B", Row: 1, Column: 2, States: map[string]bool{"enabled": true}},
		c:     {ID: c, Parent: table, Table: table, Role: "table cell", Name: "C", Row: 2, Column: 1, States: map[string]bool{"enabled": true}},
		d:     {ID: d, Parent: table, Table: table, Role: "table cell", Name: "D", Row: 2, Column: 2, States: map[string]bool{"enabled": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	engine.mu.Lock()
	engine.cursor.Object = c
	engine.mu.Unlock()
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "readTableRow"); err != nil {
		t.Fatal(err)
	}
	if got := speechTextsAfter(t, store, before); !slices.Equal(got, []string{"row 2  column 1  C", "column 2  D"}) {
		t.Fatalf("row speech = %#v", got)
	}
	if engine.State().Cursor.Object != c {
		t.Fatal("readTableRow moved cursor")
	}
	before = store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "sayAllTableRow"); err != nil {
		t.Fatal(err)
	}
	if got := speechTextsAfter(t, store, before); !slices.Equal(got, []string{"row 2  column 1  C", "column 2  D"}) {
		t.Fatalf("say-all row speech = %#v", got)
	}
	if engine.State().Cursor.Object != d {
		t.Fatal("sayAllTableRow did not move cursor to last cell")
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

func brailleAfter(t *testing.T, store *events.Store, after uint64) string {
	t.Helper()
	items, _, err := store.Snapshot(after, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range items {
		if event.Kind == events.KindBraille {
			return event.Text
		}
	}
	return ""
}

func speechTextsAfter(t *testing.T, store *events.Store, after uint64) []string {
	t.Helper()
	items, _, err := store.Snapshot(after, "test")
	if err != nil {
		t.Fatal(err)
	}
	result := make([]string, 0)
	for _, event := range items {
		if event.Kind == events.KindSpeech {
			result = append(result, event.Text)
		}
	}
	return result
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
		button:      {ID: button, Parent: application, Role: "push button", Name: "push me", Attributes: map[string]string{"tag": "button"}, States: map[string]bool{"focusable": true}},
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

func TestLiveRegionHonorsAtomicBusySettingsDeduplicationAndCausality(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	status := model.ObjectID{Bus: "app", Path: "/status"}
	statusNode := &model.Node{
		ID: status, Parent: root, Role: "statusbar", Text: "Order total updated",
		Attributes: map[string]string{"container-atomic": "true", "container-relevant": "all"},
	}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root: {ID: root, Role: "document web", Children: []model.ObjectID{status}}, status: statusNode,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	access := &fakeAccess{graph: graph, events: make(chan NativeEvent, 8)}
	store := events.NewStore(100)
	presenter, _ := profile.NewPresenter("en-US")
	engine := New(access, store, presenter, braille.Passthrough{}, synth.Silence{SampleRate: 22050}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Locale: "en-US", KeyboardLayout: "desktop"})
	ctx := context.Background()
	if err := engine.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := engine.BeginSession("test"); err != nil {
		t.Fatal(err)
	}
	if err := engine.BeginAction("test", "activate"); err != nil {
		t.Fatal(err)
	}
	change := NativeEvent{Name: "org.a11y.atspi.Event.Object.TextChanged", Source: status, Detail: "insert", Value: "updated"}
	engine.handleLiveTextChange(ctx, change)
	engine.emitLiveRegion(profile.Presentation{Speech: "Interleaved update", Braille: "Interleaved update"}, graph.Nodes[root], root, "polite")
	engine.handleLiveTextChange(ctx, change)
	engine.emitLiveRegion(profile.Presentation{Speech: "Assertive interruption", Braille: "Assertive interruption"}, graph.Nodes[root], root, "assertive")
	engine.emitLiveRegion(profile.Presentation{Speech: "Late polite update", Braille: "Late polite update"}, graph.Nodes[root], root, "polite")
	engine.EndAction("test", "activate")
	got, _, err := store.Snapshot(0, "test")
	if err != nil {
		t.Fatal(err)
	}
	liveCount := 0
	for _, event := range got {
		if event.Text == "Late polite update" {
			t.Fatalf("polite output survived assertive interruption: %#v", got)
		}
		if event.Text != "Order total updated" {
			continue
		}
		if event.CausalCommand != "activate" {
			t.Fatalf("event causality = %#v", event)
		}
		if event.Kind == events.KindLiveRegion {
			liveCount++
			if event.Provenance != events.ProvenanceAccessibilityEvent {
				t.Fatalf("live provenance = %q", event.Provenance)
			}
		}
		if event.Kind == events.KindSpeech && event.Provenance != events.ProvenanceScreenReaderOutput {
			t.Fatalf("speech provenance = %q", event.Provenance)
		}
	}
	if liveCount != 1 {
		t.Fatalf("duplicate live region events = %#v", got)
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := engine.WaitForSynthesis(waitCtx); err != nil {
		t.Fatal(err)
	}

	before := store.Cursor()
	statusNode.Attributes["container-busy"] = "true"
	engine.handleLiveTextChange(ctx, NativeEvent{Name: "org.a11y.atspi.Event.Object.TextChanged", Source: status, Detail: "insert", Value: "secret busy update"})
	if next, _, _ := store.Snapshot(before, "test"); slices.ContainsFunc(next, func(event events.Event) bool { return event.Text == "secret busy update" }) {
		t.Fatalf("busy live region emitted = %#v", next)
	}
	delete(statusNode.Attributes, "container-busy")
	settings := presenter.Settings()
	settings.ReportDynamicContentChanges = false
	if err := engine.SetPresentationSettings("test", settings); err != nil {
		t.Fatal(err)
	}
	engine.handleLiveTextChange(ctx, NativeEvent{Name: "org.a11y.atspi.Event.Object.TextChanged", Source: status, Detail: "insert", Value: "disabled update"})
	if next, _, _ := store.Snapshot(before, "test"); slices.ContainsFunc(next, func(event events.Event) bool { return event.Text == "disabled update" }) {
		t.Fatalf("disabled live reporting emitted = %#v", next)
	}
}

func TestLiveRegionResolvesInheritedAtomicContainerAndRelevantKinds(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	atomic := model.ObjectID{Bus: "app", Path: "/atomic"}
	atomicValue := model.ObjectID{Bus: "app", Path: "/atomic/value"}
	relevant := model.ObjectID{Bus: "app", Path: "/relevant"}
	added := model.ObjectID{Bus: "app", Path: "/relevant/added"}
	dynamic := model.ObjectID{Bus: "app", Path: "/relevant/dynamic"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root: {ID: root, Role: "document web", Children: []model.ObjectID{atomic, relevant}},
		atomic: {
			ID: atomic, Parent: root, Role: "section", Text: "Atomic total", Children: []model.ObjectID{atomicValue},
			Attributes: map[string]string{"live": "polite", "atomic": "true"},
		},
		atomicValue: {
			ID: atomicValue, Parent: atomic, Role: "static", Text: "pending",
			Attributes: map[string]string{"container-live": "polite", "container-atomic": "true"},
		},
		relevant: {
			ID: relevant, Parent: root, Role: "section", Children: []model.ObjectID{added},
			Attributes: map[string]string{"live": "polite", "relevant": "additions"},
		},
		added: {
			ID: added, Parent: relevant, Role: "static", Text: "Parity relevant addition",
			Attributes: map[string]string{"container-live": "polite", "container-relevant": "additions", "relevant": "additions text"},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	// A direct AT-SPI read does not populate Parent. The cached graph must be
	// used to resolve inherited container-* attributes back to their owner.
	freshAtomicValue := *graph.Nodes[atomicValue]
	freshAtomicValue.Parent = model.ObjectID{}
	freshAtomicValue.Text = "ready"
	freshAtomicValue.Relations = map[string][]model.ObjectID{"member of": {atomic}}
	dynamicNode := &model.Node{
		ID: dynamic, Role: "static", Text: "Dynamic addition",
		Attributes: map[string]string{"container-live": "polite", "container-relevant": "additions", "relevant": "additions text"},
	}
	access := &fakeAccess{
		graph: graph,
		readNodes: map[model.ObjectID]*model.Node{
			atomicValue: &freshAtomicValue,
			dynamic:     dynamicNode,
		},
		events: make(chan NativeEvent, 1),
	}
	store := events.NewStore(100)
	presenter, _ := profile.NewPresenter("en-US")
	engine := New(access, store, presenter, braille.Passthrough{}, synth.Silence{SampleRate: 22050}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Locale: "en-US", KeyboardLayout: "desktop"})
	ctx := context.Background()
	if err := engine.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := engine.BeginSession("test"); err != nil {
		t.Fatal(err)
	}

	before := store.Cursor()
	// Simulate Chromium delivering a new text object and owner before the cached
	// graph refresh. The direct object still identifies its live container
	// through MEMBER_OF; BrowserGraph exposes the current active-document pair.
	staleGraph := *engine.graph
	staleGraph.Nodes = make(map[model.ObjectID]*model.Node, len(engine.graph.Nodes)-2)
	for id, node := range engine.graph.Nodes {
		if id != atomic && id != atomicValue {
			staleGraph.Nodes[id] = node
		}
	}
	engine.graph = &staleGraph
	engine.handleLiveTextChange(ctx, NativeEvent{Name: "TextChanged", Source: atomicValue, Detail: "insert", Value: "ready"})
	if got := speechAfter(t, store, before); got != "Atomic total ready" {
		t.Fatalf("atomic descendant speech = %q", got)
	}
	if access.graphReads != 2 {
		t.Fatalf("graph refreshes = %d, want startup plus live-owner refresh", access.graphReads)
	}

	before = store.Cursor()
	engine.handleLiveTextChange(ctx, NativeEvent{Name: "TextChanged", Source: added, Detail: "insert", Value: "changed text"})
	if next, _, snapshotErr := store.Snapshot(before, "test"); snapshotErr != nil || slices.ContainsFunc(next, func(event events.Event) bool { return event.Text == "changed text" }) {
		t.Fatalf("additions-only region emitted text change: events=%#v err=%v", next, snapshotErr)
	}

	engine.handleLiveChildrenChange(ctx, NativeEvent{
		Name: "ChildrenChanged", Source: relevant, Detail: "add", ValueObject: added,
	})
	if got := speechAfter(t, store, before); got != "Parity relevant addition" {
		t.Fatalf("relevant child addition speech = %q", got)
	}

	before = store.Cursor()
	engine.handleLiveTextChange(ctx, NativeEvent{Name: "TextChanged", Source: dynamic, Detail: "insert:system", Value: "Dynamic addition"})
	if got := speechAfter(t, store, before); got != "Dynamic addition" {
		t.Fatalf("unknown accessible addition speech = %q", got)
	}
	before = store.Cursor()
	dynamicNode.Text = "Dynamic text edit"
	engine.handleLiveTextChange(ctx, NativeEvent{Name: "TextChanged", Source: dynamic, Detail: "insert", Value: "Dynamic text edit"})
	if next, _, snapshotErr := store.Snapshot(before, "test"); snapshotErr != nil || slices.ContainsFunc(next, func(event events.Event) bool { return event.Text == "Dynamic text edit" }) {
		t.Fatalf("known additions-only object emitted text edit: events=%#v err=%v", next, snapshotErr)
	}

	before = store.Cursor()
	graph.Nodes[added].Text = "Container-sourced addition"
	engine.handleLiveTextChange(ctx, NativeEvent{Name: "TextChanged", Source: relevant, Detail: "insert:system", Value: "\ufffc"})
	if got := speechAfter(t, store, before); got != "Container-sourced addition" {
		t.Fatalf("container-sourced addition speech = %q", got)
	}
}
