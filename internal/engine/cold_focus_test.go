package engine

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
)

func TestRefreshRecoversForegroundEditableBeforeItsNativeEvent(t *testing.T) {
	e, store := fixtureEngine(t)
	id := func(p string) model.ObjectID { return model.ObjectID{Bus: "app", Path: p} }
	app, front, back := id("/chrome"), id("/front"), id("/back")
	doc, other, entry, stale, heading := id("/catalog"), id("/background"), id("/search"), id("/other-search"), id("/heading2")
	g, err := model.NewGraph(app, map[model.ObjectID]*model.Node{
		app:     {ID: app, Role: "application", Children: []model.ObjectID{back, front}},
		front:   {ID: front, Parent: app, Role: "frame", States: map[string]bool{"active": true}, Children: []model.ObjectID{doc}},
		back:    {ID: back, Parent: app, Role: "frame", Children: []model.ObjectID{other}},
		doc:     {ID: doc, Parent: front, Role: "document web", Name: "Standards catalogue", Children: []model.ObjectID{entry, heading}},
		other:   {ID: other, Parent: back, Role: "document web", Name: "Background", Children: []model.ObjectID{stale}},
		stale:   {ID: stale, Parent: other, Role: "entry", Name: "Wrong window", States: map[string]bool{"focused": true, "editable": true}},
		entry:   {ID: entry, Parent: doc, Role: "entry", Name: "Search technical reports", States: map[string]bool{"focused": true, "editable": true, "focusable": true}},
		heading: {ID: heading, Parent: doc, Role: "heading", Name: "Last heading", HeadingLevel: 2},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	access := e.access.(*fakeAccess)
	access.graph = g
	e.handleNativeEvent(context.Background(), NativeEvent{Name: "org.a11y.atspi.Event.Document.LoadComplete", Source: doc})
	if err := e.ExecuteDirect(context.Background(), "readCurrent"); err != nil {
		t.Fatal(err)
	}
	st := e.State()
	if st.Focus != entry || st.Cursor.Object != entry || st.Cursor.Mode != "focus" {
		t.Fatalf("native focus=%s, cursor=%#v; want foreground entry before queued event", st.Focus, st.Cursor)
	}
	records, _, _ := store.Snapshot(0, "test")
	for _, kind := range []events.Kind{events.KindSpeech, events.KindBraille} {
		found := false
		for _, ev := range records {
			if ev.Kind == kind && ev.CausalCommand == "readCurrent" && strings.Contains(ev.Text, "Search technical reports") {
				found = true
			}
		}
		if !found {
			t.Errorf("missing search read in %v", kind)
		}
	}
	// A stale editable state inside the tab must not override the browser's
	// actual focus after the user moves to chrome.
	e.mu.Lock()
	e.focus, e.cursor = front, model.Cursor{Object: doc, Mode: "browse"}
	e.webContentFocused = false
	e.mu.Unlock()
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := e.State(); st.Focus != front || st.WebContentFocused {
		t.Fatalf("refresh stole focus from browser chrome: %#v", st)
	}
	// A deliberate browse cursor independent of DOM focus must stay put.
	e.mu.Lock()
	e.cursor = model.Cursor{Object: heading, Mode: "browse"}
	e.mu.Unlock()
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := e.State(); st.Cursor.Object != heading || st.Cursor.Mode != "browse" {
		t.Fatalf("refresh moved deliberate browse cursor: %#v", st.Cursor)
	}
}

func TestFocusedEntryInNewDocumentDoesNotWaitForFullGraph(t *testing.T) {
	e, store := fixtureEngine(t)
	access := e.access.(*fakeAccess)
	doc := model.ObjectID{Bus: "app", Path: "/new-doc"}
	entry := model.ObjectID{Bus: "app", Path: "/new-search"}
	access.readNodes = map[model.ObjectID]*model.Node{doc: {ID: doc, Role: "document web", Name: "New large page"}, entry: {ID: entry, Role: "entry", Name: "Search technical reports", Attributes: map[string]string{"tag": "input"}, States: map[string]bool{"focused": true, "editable": true}}}
	// The previous document has gone away; the native parent chain is complete
	// to its new document but not to a node in the last installed graph.
	app := model.ObjectID{Bus: "app", Path: "/chrome"}
	frame := model.ObjectID{Bus: "app", Path: "/front"}
	access.readNodes[frame] = &model.Node{ID: frame, Role: "frame", States: map[string]bool{"active": true}}
	access.readNodes[app] = &model.Node{ID: app, Role: "application"}
	access.parents = map[model.ObjectID]model.ObjectID{entry: doc, doc: frame, frame: app}
	access.blockGraphs = true
	before := access.graphReads
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	e.handleNativeEvent(ctx, NativeEvent{Name: "org.a11y.atspi.Event.Object.StateChanged", Detail: "focused", Detail1: 1, Source: entry})
	if access.graphReads != before {
		t.Fatalf("first focus blocked on %d full graph read(s)", access.graphReads-before)
	}
	out, _, _ := store.Snapshot(0, "test")
	for _, ev := range out {
		if ev.Kind == events.KindFocus && ev.Text == "Search technical reports" {
			return
		}
	}
	t.Fatal("no spontaneous entry focus")
}

func TestLiveFocusPreservesContextAndProtectsRelatedContent(t *testing.T) {
	e, _ := fixtureEngine(t)
	a := e.access.(*fakeAccess)
	id := func(path string) model.ObjectID { return model.ObjectID{Bus: "app", Path: path} }
	app, frame, doc, group, cell, header, description := id("/app"), id("/frame"), id("/doc"), id("/group"), id("/cell"), id("/header"), id("/description")
	a.readNodes = map[model.ObjectID]*model.Node{
		app:         {ID: app, Role: "application", Children: []model.ObjectID{frame}},
		frame:       {ID: frame, Role: "frame", States: map[string]bool{"active": true}, Children: []model.ObjectID{doc}},
		doc:         {ID: doc, Role: "document web", Children: []model.ObjectID{group}},
		group:       {ID: group, Role: "panel", Name: "Measurements", Attributes: map[string]string{"xml-roles": "application"}, Children: []model.ObjectID{cell, header, description}},
		cell:        {ID: cell, Role: "table cell", Name: "42", States: map[string]bool{"focused": true}, Attributes: map[string]string{"tag": "td"}, ColumnHeaders: []model.ObjectID{header}, Relations: map[string][]model.ObjectID{"described by": {description}, "details": {description}}},
		header:      {ID: header, Role: "column header", Name: "Temperature"},
		description: {ID: description, Role: "section", Text: "In degrees Celsius"},
	}
	a.parents = map[model.ObjectID]model.ObjectID{cell: group, header: group, description: group, group: doc, doc: frame, frame: app}
	path, bg := e.foregroundWebPath(context.Background(), e.graph, a.readNodes[cell])
	if bg || len(path) != 5 || !e.resolveFocusPathPresentation(context.Background(), path) {
		t.Fatal("could not resolve complete foreground context")
	}
	leaf := path[len(path)-1]
	if !inWebApplication(path) || leaf.Parent != group || !slices.Equal(leaf.ColumnHeaderText, []string{"Temperature"}) || !slices.Equal(leaf.RelationText["described by"], []string{"In degrees Celsius"}) || !slices.Equal(leaf.RelationText["details"], []string{"In degrees Celsius"}) {
		t.Fatalf("lost focus metadata: %#v", leaf)
	}
	if a.readNodes[cell].Parent.Valid() || len(a.readNodes[cell].RelationText) != 0 {
		t.Fatal("mutated direct AT-SPI read")
	}
	// A parent can retain a stale child ID while that accessible moves to a
	// different, possibly protected subtree.
	a.parents[description] = id("/moved")
	path, _ = e.foregroundWebPath(context.Background(), e.graph, a.readNodes[cell])
	if e.resolveFocusPathPresentation(context.Background(), path) {
		t.Fatal("accepted a relation target whose live parent changed")
	}
	a.parents[description] = group
	// An unanchored relation may be on a different page; the complete graph
	// must resolve it. A protected ancestor covers the focused leaf itself.
	a.readNodes[cell].Relations["described by"] = []model.ObjectID{id("/unrelated")}
	path, _ = e.foregroundWebPath(context.Background(), e.graph, a.readNodes[cell])
	if e.resolveFocusPathPresentation(context.Background(), path) {
		t.Fatal("accepted unverified relation target")
	}
	a.readNodes[group].Role = "password text"
	path, _ = e.foregroundWebPath(context.Background(), e.graph, a.readNodes[cell])
	if !path[len(path)-1].IsProtected() || path[len(path)-1].Name != "[redacted]" {
		t.Fatal("direct focus output lost ancestor protection")
	}
	if current, txt := e.readLiveRegionContent(context.Background(), path[len(path)-1]); current == nil || !current.IsProtected() || txt != "[redacted]" {
		t.Fatal("reread disclosed protected content from a verified live ancestor")
	}
}

func TestLateBackgroundFocusDoesNotMoveForeground(t *testing.T) {
	e, store := fixtureEngine(t)
	a := e.access.(*fakeAccess)
	id := func(s string) model.ObjectID { return model.ObjectID{Bus: "app", Path: s} }
	app, front, back, doc, bdoc, focused, stale := id("/app"), id("/front"), id("/background"), id("/doc"), id("/old-doc"), id("/field"), id("/other-field")
	nodes := map[model.ObjectID]*model.Node{
		app:     {ID: app, Role: "application", Children: []model.ObjectID{front, back}},
		front:   {ID: front, Parent: app, Role: "frame", Children: []model.ObjectID{doc}, States: map[string]bool{"active": true}},
		back:    {ID: back, Parent: app, Role: "frame", Children: []model.ObjectID{bdoc}},
		doc:     {ID: doc, Parent: front, Role: "document web", Children: []model.ObjectID{focused}},
		bdoc:    {ID: bdoc, Parent: back, Role: "document web", Children: []model.ObjectID{stale}},
		focused: {ID: focused, Parent: doc, Role: "entry", Name: "Foreground", States: map[string]bool{"focused": true, "editable": true}, Attributes: map[string]string{"tag": "input"}},
		stale:   {ID: stale, Parent: bdoc, Role: "entry", Name: "Background", States: map[string]bool{"focused": true, "editable": true}, Attributes: map[string]string{"tag": "input"}},
	}
	g, err := model.NewGraph(app, nodes, 2)
	if err != nil {
		t.Fatal(err)
	}
	a.graph = g
	a.parents = map[model.ObjectID]model.ObjectID{stale: bdoc, bdoc: back, back: app, focused: doc, doc: front, front: app}
	if err := e.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.handleFocus(context.Background(), focused)
	before := store.Cursor()
	e.handleFocus(context.Background(), stale)
	e.handleNativeEvent(context.Background(), NativeEvent{Name: "org.a11y.atspi.Event.Document.LoadComplete", Source: bdoc})
	if e.State().Focus != focused {
		t.Fatalf("stale background window overrode foreground: %#v", e.State())
	}
	out, _, _ := store.Snapshot(before, "test")
	for _, v := range out {
		if v.Kind == events.KindFocus {
			t.Fatal("background focus was announced")
		}
	}
}

func TestAmbientAnnouncementsStayOutsideReportingActions(t *testing.T) {
	e, store := fixtureEngine(t)
	id := model.ObjectID{Bus: "app", Path: "/button"}
	if err := e.BeginAction("test", "readCurrent"); err != nil {
		t.Fatal(err)
	}
	e.handleNativeEvent(context.Background(), NativeEvent{Name: "org.a11y.atspi.Event.Object.Announcement", Source: id, Value: "Unrelated catalogue announcement"})
	if err := e.ExecuteDirect(context.Background(), "readCurrent"); err != nil {
		t.Fatal(err)
	}
	e.EndAction("test", "readCurrent")
	out, _, _ := store.Snapshot(0, "test")
	var reports, announcements int
	for _, v := range out {
		if v.Kind == events.KindSpeech {
			switch v.Reason {
			case "report":
				if v.CausalCommand == "readCurrent" {
					reports++
				}
			case "liveRegion":
				if v.CausalCommand != "event" {
					t.Fatalf("ambient page speech attributed to %q", v.CausalCommand)
				}
				announcements++
			}
		}
	}
	if reports != 1 || announcements != 1 {
		t.Fatalf("report=%d, ambient announcements=%d", reports, announcements)
	}
}

type focusQueueAccess struct {
	*fakeAccess
	focus chan NativeEvent
}

func (f *focusQueueAccess) FocusEvents() <-chan NativeEvent { return f.focus }

func TestSupersededNativeFocusGainDoesNotAnnounceOldField(t *testing.T) {
	e, store := fixtureEngine(t)
	a := &focusQueueAccess{fakeAccess: e.access.(*fakeAccess), focus: make(chan NativeEvent)}
	e.access = a
	before := store.Cursor()
	button := model.ObjectID{Bus: "app", Path: "/button"}
	// There is still an AT-SPI object, but its current focused bit is clear.
	e.handleNativeEvent(context.Background(), NativeEvent{Name: "org.a11y.atspi.Event.Object.StateChanged", Detail: "focused", Detail1: 1, Source: button})
	seen, _, _ := store.Snapshot(before, "test")
	for _, v := range seen {
		if v.Kind == events.KindFocus || v.Kind == events.KindSpeech || v.Kind == events.KindBraille {
			t.Fatalf("queued obsolete focus gain was presented: %#v", v)
		}
	}
}
