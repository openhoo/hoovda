package engine

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/openhoo/hoovda/internal/braille"
	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
	"github.com/openhoo/hoovda/internal/profile"
	"github.com/openhoo/hoovda/internal/synth"
)

func TestNativeFocusAnnouncesEnteredNamedWebGroup(t *testing.T) {
	document := model.ObjectID{Bus: "app", Path: "/document"}
	group := model.ObjectID{Bus: "app", Path: "/calendar"}
	day := model.ObjectID{Bus: "app", Path: "/calendar/day"}
	graph, err := model.NewGraph(document, map[model.ObjectID]*model.Node{
		document: {ID: document, Role: "document web", Children: []model.ObjectID{group}},
		group: {
			ID: group, Parent: document, Role: "panel", Name: "Choose a day: November 2026",
			Attributes: map[string]string{"xml-roles": "group"}, Children: []model.ObjectID{day},
			States: map[string]bool{"enabled": true},
		},
		day: {ID: day, Parent: group, Role: "toggle button", Name: "16 November", States: map[string]bool{"focusable": true, "enabled": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	// AT-SPI direct reads do not carry the traversal's parent links.
	directDay := *graph.Nodes[day]
	directDay.Parent = model.ObjectID{}
	value, access, store := nativeFocusEngine(t, graph, map[model.ObjectID]*model.Node{day: &directDay})
	items := nativeFocusOutput(t, value, access, store, day)
	var speeches []events.Event
	var focuses []events.Event
	for _, item := range items {
		if item.Kind == events.KindSpeech {
			speeches = append(speeches, item)
		}
		if item.Kind == events.KindFocus {
			focuses = append(focuses, item)
		}
	}
	const want = "Choose a day: November 2026  grouping  16 November  toggle button  not pressed"
	if len(speeches) != 1 || speeches[0].Text != want {
		t.Fatalf("native focus speech = %#v, want one combined %q", speeches, want)
	}
	if speech := speeches[0]; speech.Source == nil || *speech.Source != day || speech.Reason != "nativeFocus" || speech.Provenance != events.ProvenanceScreenReaderOutput {
		t.Fatalf("focus speech lost native attribution: %#v", speech)
	}
	if len(focuses) != 1 || focuses[0].Source == nil || *focuses[0].Source != day || focuses[0].Provenance != events.ProvenanceAccessibilityEvent {
		t.Fatalf("native focus events = %#v", focuses)
	}
}

func TestNativeFocusAnnouncesFieldsetLegend(t *testing.T) {
	document := model.ObjectID{Bus: "app", Path: "/document"}
	fieldset := model.ObjectID{Bus: "app", Path: "/personal-details"}
	entry := model.ObjectID{Bus: "app", Path: "/personal-details/name"}
	graph, err := model.NewGraph(document, map[model.ObjectID]*model.Node{
		document: {ID: document, Role: "document web", Children: []model.ObjectID{fieldset}},
		fieldset: {
			ID: fieldset, Parent: document, Role: "panel", Name: "Personal details",
			Attributes: map[string]string{"tag": "fieldset"}, Children: []model.ObjectID{entry},
			States: map[string]bool{"enabled": true},
		},
		entry: {ID: entry, Parent: fieldset, Role: "entry", Name: "Name", States: map[string]bool{"focusable": true, "enabled": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	value, access, store := nativeFocusEngine(t, graph, nil)
	items := nativeFocusOutput(t, value, access, store, entry)
	if got := focusSpeech(items); got != "Personal details  grouping  Name  entry" {
		t.Fatalf("fieldset focus speech = %q", got)
	}
}

func TestNativeFocusAnnouncesDialogNameAndExplicitDescription(t *testing.T) {
	document := model.ObjectID{Bus: "app", Path: "/document"}
	dialog := model.ObjectID{Bus: "app", Path: "/cancel-dialog"}
	closeButton := model.ObjectID{Bus: "app", Path: "/cancel-dialog/close"}
	graph, err := model.NewGraph(document, map[model.ObjectID]*model.Node{
		document: {ID: document, Role: "document web", Children: []model.ObjectID{dialog}},
		dialog: {
			ID: dialog, Parent: document, Role: "dialog", Name: "Cancel appointment",
			Description: "Please confirm cancellation", Text: "Entire dialog contents must not become focus context",
			RelationText: map[string][]string{"described by": {"Please confirm cancellation"}},
			Children:     []model.ObjectID{closeButton}, States: map[string]bool{"enabled": true},
		},
		closeButton: {ID: closeButton, Parent: dialog, Role: "push button", Name: "Close", States: map[string]bool{"focusable": true, "enabled": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	value, access, store := nativeFocusEngine(t, graph, nil)
	items := nativeFocusOutput(t, value, access, store, closeButton)
	if got := focusSpeech(items); got != "Cancel appointment  dialog  Please confirm cancellation  Close  button" {
		t.Fatalf("dialog focus speech = %q", got)
	}
}

func TestNativeFocusOnlyAnnouncesNewContextAndRepeatsItOnReentry(t *testing.T) {
	id := func(path string) model.ObjectID { return model.ObjectID{Bus: "app", Path: path} }
	document, outside, outer, inner, first, second, alternative, place := id("/document"), id("/outside"), id("/outer"), id("/inner"), id("/first"), id("/second"), id("/alternative"), id("/place")
	enabled := map[string]bool{"enabled": true}
	focusable := map[string]bool{"enabled": true, "focusable": true}
	graph, err := model.NewGraph(document, map[model.ObjectID]*model.Node{
		document:    {ID: document, Role: "document web", Children: []model.ObjectID{outside, outer}},
		outside:     {ID: outside, Parent: document, Role: "push button", Name: "Before", States: focusable},
		outer:       {ID: outer, Parent: document, Role: "grouping", Name: "Appointments", Children: []model.ObjectID{inner, alternative}, States: enabled},
		inner:       {ID: inner, Parent: outer, Role: "grouping", Name: "Time", Children: []model.ObjectID{first, second}, States: enabled},
		first:       {ID: first, Parent: inner, Role: "entry", Name: "First", States: focusable},
		second:      {ID: second, Parent: inner, Role: "entry", Name: "Second", States: focusable},
		alternative: {ID: alternative, Parent: outer, Role: "grouping", Name: "Place", Children: []model.ObjectID{place}, States: enabled},
		place:       {ID: place, Parent: alternative, Role: "entry", Name: "Location", States: focusable},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	value, access, store := nativeFocusEngine(t, graph, nil)
	for _, step := range []struct {
		name   string
		target model.ObjectID
		want   string
	}{
		{"outside", outside, "Before  button"},
		{"nested entry", first, "Appointments  grouping  Time  grouping  First  entry"},
		{"duplicate focus", first, "First  entry"},
		{"sibling", second, "Second  entry"},
		{"other nested group", place, "Place  grouping  Location  entry"},
		{"direct parent", outer, "Appointments  grouping"},
		{"parent to child", first, "Time  grouping  First  entry"},
		{"leave", outside, "Before  button"},
		{"reenter", first, "Appointments  grouping  Time  grouping  First  entry"},
	} {
		t.Run(step.name, func(t *testing.T) {
			items := nativeFocusOutput(t, value, access, store, step.target)
			if got := focusSpeech(items); got != step.want {
				t.Fatalf("focus speech = %q, want %q", got, step.want)
			}
		})
	}
	if access.graphReads != 1 {
		t.Fatalf("unchanged focus paths refreshed the full graph %d times", access.graphReads)
	}
}

func TestNativeFocusRefreshesChangedContextForKnownTarget(t *testing.T) {
	document := model.ObjectID{Bus: "app", Path: "/document"}
	oldGroup := model.ObjectID{Bus: "app", Path: "/old-group"}
	newGroup := model.ObjectID{Bus: "app", Path: "/new-group"}
	button := model.ObjectID{Bus: "app", Path: "/continue"}
	makeGraph := func(parent model.ObjectID, name string, revision uint64) *model.Graph {
		nodes := map[model.ObjectID]*model.Node{
			document: {ID: document, Role: "document web", Children: []model.ObjectID{oldGroup, newGroup}},
			oldGroup: {ID: oldGroup, Parent: document, Role: "grouping", Name: name, States: map[string]bool{"enabled": true}},
			newGroup: {ID: newGroup, Parent: document, Role: "grouping", Name: "New group", States: map[string]bool{"enabled": true}},
			button:   {ID: button, Parent: parent, Role: "push button", Name: "Continue", States: map[string]bool{"enabled": true, "focusable": true}},
		}
		nodes[parent].Children = []model.ObjectID{button}
		graph, err := model.NewGraph(document, nodes, revision)
		if err != nil {
			t.Fatal(err)
		}
		return graph
	}
	for _, test := range []struct {
		name   string
		event  NativeEvent
		parent model.ObjectID
		label  string
		want   string
	}{
		{"reparented", NativeEvent{Name: "org.a11y.atspi.Event.Object.ChildrenChanged", Source: newGroup, Detail: "add", ValueObject: button}, newGroup, "Old group", "New group  grouping  Continue  button"},
		{"renamed", NativeEvent{Name: "org.a11y.atspi.Event.Object.PropertyChange", Source: oldGroup, Detail: "accessible-name"}, oldGroup, "Renamed group", "Renamed group  grouping  Continue  button"},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldGraph := makeGraph(oldGroup, "Old group", 1)
			newGraph := makeGraph(test.parent, test.label, 2)
			directButton := *newGraph.Nodes[button]
			directButton.Parent = model.ObjectID{}
			value, access, store := nativeFocusEngine(t, oldGraph, map[model.ObjectID]*model.Node{button: &directButton})
			access.graphAtRead = func(int) *model.Graph { return newGraph }
			access.events <- test.event
			if got := focusSpeech(nativeFocusOutput(t, value, access, store, button)); got != test.want {
				t.Fatalf("changed known context = %q, want %q", got, test.want)
			}
			if access.graphReads != 2 {
				t.Fatalf("graph reads = %d, want one bounded context refresh", access.graphReads)
			}
		})
	}
}

func TestNativeFocusRetainsCommonContextWhenRefreshRemovesPreviousFocus(t *testing.T) {
	document := model.ObjectID{Bus: "app", Path: "/document"}
	group := model.ObjectID{Bus: "app", Path: "/group"}
	first := model.ObjectID{Bus: "app", Path: "/first"}
	second := model.ObjectID{Bus: "app", Path: "/second"}
	makeGraph := func(child model.ObjectID, name string, revision uint64) *model.Graph {
		graph, err := model.NewGraph(document, map[model.ObjectID]*model.Node{
			document: {ID: document, Role: "document web", Children: []model.ObjectID{group}},
			group:    {ID: group, Parent: document, Role: "grouping", Name: "Shared", Children: []model.ObjectID{child}, States: map[string]bool{"enabled": true}},
			child:    {ID: child, Parent: group, Role: "push button", Name: name, States: map[string]bool{"enabled": true, "focusable": true}},
		}, revision)
		if err != nil {
			t.Fatal(err)
		}
		return graph
	}
	oldGraph, newGraph := makeGraph(first, "First", 1), makeGraph(second, "Second", 2)
	directSecond := *newGraph.Nodes[second]
	directSecond.Parent = model.ObjectID{}
	value, access, store := nativeFocusEngine(t, oldGraph, map[model.ObjectID]*model.Node{second: &directSecond})
	access.graphAtRead = func(int) *model.Graph { return newGraph }
	if got := focusSpeech(nativeFocusOutput(t, value, access, store, first)); got != "Shared  grouping  First  button" {
		t.Fatalf("initial group focus = %q", got)
	}
	if got := focusSpeech(nativeFocusOutput(t, value, access, store, second)); got != "Second  button" {
		t.Fatalf("refreshed sibling repeated shared context = %q", got)
	}
	if revision := value.State().GraphRevision; revision != 2 {
		t.Fatalf("graph revision = %d, want refreshed graph", revision)
	}
}

func TestNativeFocusDiscoversNewDialogContextOnRefresh(t *testing.T) {
	document := model.ObjectID{Bus: "app", Path: "/document"}
	dialog := model.ObjectID{Bus: "app", Path: "/dialog"}
	button := model.ObjectID{Bus: "app", Path: "/dialog/close"}
	oldGraph, err := model.NewGraph(document, map[model.ObjectID]*model.Node{
		document: {ID: document, Role: "document web"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	newGraph, err := model.NewGraph(document, map[model.ObjectID]*model.Node{
		document: {ID: document, Role: "document web", Children: []model.ObjectID{dialog}},
		dialog:   {ID: dialog, Parent: document, Role: "dialog", Name: "New dialog", Description: "Explicit help", Children: []model.ObjectID{button}, States: map[string]bool{"enabled": true}},
		button:   {ID: button, Parent: dialog, Role: "push button", Name: "Close", States: map[string]bool{"enabled": true, "focusable": true}},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	directButton := *newGraph.Nodes[button]
	directButton.Parent = model.ObjectID{}
	value, access, store := nativeFocusEngine(t, oldGraph, map[model.ObjectID]*model.Node{button: &directButton})
	access.graphAtRead = func(int) *model.Graph { return newGraph }
	if got := focusSpeech(nativeFocusOutput(t, value, access, store, button)); got != "New dialog  dialog  Explicit help  Close  button" {
		t.Fatalf("new dialog context = %q", got)
	}
}

func TestNativeFocusKeepsDialogContextWhenFullRefreshTimesOut(t *testing.T) {
	id := func(path string) model.ObjectID { return model.ObjectID{Bus: "app", Path: path} }
	doc, dialog, first, next := id("/doc"), id("/dialog"), id("/cancel"), id("/archive")
	graph, err := model.NewGraph(doc, map[model.ObjectID]*model.Node{
		doc: {ID: doc, Role: "document web"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Chromium's direct reads do not populate Parent. The live AT-SPI
	// property is available even when the full tree cannot be refreshed.
	direct := map[model.ObjectID]*model.Node{
		dialog: {ID: dialog, Role: "dialog", Name: "Archive notebook?",
			Description: "You can restore it later.", States: map[string]bool{"enabled": true}},
		first: {ID: first, Role: "push button", Name: "Cancel",
			States: map[string]bool{"focusable": true, "enabled": true}, Attributes: map[string]string{"tag": "button"}},
		next: {ID: next, Role: "push button", Name: "Archive",
			States: map[string]bool{"focusable": true, "enabled": true}, Attributes: map[string]string{"tag": "button"}},
	}
	value, access, store := nativeFocusEngine(t, graph, direct)
	access.parents = map[model.ObjectID]model.ObjectID{dialog: doc, first: dialog, next: dialog}
	access.graphErr = context.DeadlineExceeded
	if got := focusSpeech(nativeFocusOutput(t, value, access, store, first)); got != "Archive notebook?  dialog  You can restore it later.  Cancel  button" {
		t.Fatalf("first focus after opening the dialog = %q", got)
	}
	if got := focusSpeech(nativeFocusOutput(t, value, access, store, next)); got != "Archive  button" {
		t.Fatalf("focus within the same dialog = %q", got)
	}
	if state := value.State(); state.Focus != next || state.Cursor.Object != doc || state.GraphRevision != graph.Revision {
		t.Fatalf("partial focus path changed the navigation graph: %+v", state)
	}
}

func TestNativeFocusChecksRecoveredAncestorPath(t *testing.T) {
	id := func(path string) model.ObjectID { return model.ObjectID{Bus: "app", Path: path} }
	doc, ancestor, dialog, button := id("/doc"), id("/ancestor"), id("/dialog"), id("/button")
	for _, test := range []struct {
		name    string
		parent  model.ObjectID
		protect bool
		want    string
	}{
		{"protected parent", doc, true, "[redacted]"},
		{"detached protected parent", id("/detached"), true, "[redacted]"},
		{"cycle", dialog, false, "Choose  button"},
		{"unattached path", id("/detached"), false, "Choose  button"},
		{"different application", model.ObjectID{Bus: "other-app", Path: "/doc"}, false, "Choose  button"},
	} {
		t.Run(test.name, func(t *testing.T) {
			graph, err := model.NewGraph(doc, map[model.ObjectID]*model.Node{
				doc: {ID: doc, Role: "document web"},
			}, 1)
			if err != nil {
				t.Fatal(err)
			}
			direct := map[model.ObjectID]*model.Node{
				ancestor: {ID: ancestor, Role: "panel", Redacted: test.protect},
				dialog:   {ID: dialog, Role: "dialog", Name: "Unverified context"},
				button:   {ID: button, Role: "push button", Name: "Choose", States: map[string]bool{"enabled": true}, Attributes: map[string]string{"tag": "button"}},
			}
			value, access, store := nativeFocusEngine(t, graph, direct)
			access.graphErr = context.DeadlineExceeded
			access.parents = map[model.ObjectID]model.ObjectID{button: dialog, dialog: ancestor, ancestor: test.parent}
			items := nativeFocusOutput(t, value, access, store, button)
			if got := focusSpeech(items); !strings.Contains(got, test.want) {
				t.Fatalf("focus speech = %q, want %q", got, test.want)
			}
			for _, item := range items {
				if strings.Contains(item.Text, "Unverified context") || (test.protect && strings.Contains(item.Text, "Choose")) {
					t.Fatalf("unexpected context in %s: %q", item.Kind, item.Text)
				}
			}
		})
	}
}

func TestNativeFocusContextStartsFreshInEachSession(t *testing.T) {
	document := model.ObjectID{Bus: "app", Path: "/document"}
	group := model.ObjectID{Bus: "app", Path: "/group"}
	button := model.ObjectID{Bus: "app", Path: "/group/continue"}
	graph, err := model.NewGraph(document, map[model.ObjectID]*model.Node{
		document: {ID: document, Role: "document web", Children: []model.ObjectID{group}},
		group:    {ID: group, Parent: document, Role: "grouping", Name: "Choice", Children: []model.ObjectID{button}, States: map[string]bool{"enabled": true}},
		button:   {ID: button, Parent: group, Role: "push button", Name: "Continue", States: map[string]bool{"enabled": true, "focusable": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	value, access, store := nativeFocusEngine(t, graph, nil)
	for session := 0; session < 2; session++ {
		if got := focusSpeech(nativeFocusOutput(t, value, access, store, button)); got != "Choice  grouping  Continue  button" {
			t.Fatalf("session %d focus = %q", session, got)
		}
		value.EndSession("test")
		if session == 0 {
			if err := value.BeginSession("test"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestNativeFocusContextUsesOneCompleteSynthesisRequest(t *testing.T) {
	document := model.ObjectID{Bus: "app", Path: "/document"}
	group := model.ObjectID{Bus: "app", Path: "/group"}
	button := model.ObjectID{Bus: "app", Path: "/group/continue"}
	graph, err := model.NewGraph(document, map[model.ObjectID]*model.Node{
		document: {ID: document, Role: "document web", Children: []model.ObjectID{group}},
		group:    {ID: group, Parent: document, Role: "grouping", Name: "Choice", Children: []model.ObjectID{button}, States: map[string]bool{"enabled": true}},
		button:   {ID: button, Parent: group, Role: "push button", Name: "Continue", States: map[string]bool{"enabled": true, "focusable": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	value, access, store := nativeFocusEngine(t, graph, nil)
	driver := gatedSynthesis{started: make(chan string, 4), release: make(chan struct{})}
	value.synth = driver
	items := nativeFocusOutput(t, value, access, store, button)
	if len(driver.started) != 1 {
		t.Fatalf("synthesis requests = %d, want one complete utterance", len(driver.started))
	}
	if text := <-driver.started; text != "Choice  grouping  Continue  button" || text != focusSpeech(items) {
		t.Fatalf("synthesized focus text = %q; speech = %q", text, focusSpeech(items))
	}
}

func TestNativeFocusContextDoesNotExposeProtectedAncestorContent(t *testing.T) {
	document := model.ObjectID{Bus: "app", Path: "/document"}
	password := model.ObjectID{Bus: "app", Path: "/password"}
	group := model.ObjectID{Bus: "app", Path: "/password/group"}
	child := model.ObjectID{Bus: "app", Path: "/password/group/child"}
	rawChild := &model.Node{ID: child, Parent: group, Role: "static", Name: "raw-secret-child", States: map[string]bool{"enabled": true}}
	graph, err := model.NewGraph(document, map[model.ObjectID]*model.Node{
		document: {ID: document, Role: "document web", Children: []model.ObjectID{password}},
		password: {ID: password, Parent: document, Role: "password text", Name: "Password", Children: []model.ObjectID{group}},
		group:    {ID: group, Parent: password, Role: "grouping", Name: "raw-secret-group", Description: "raw-secret-description", Children: []model.ObjectID{child}},
		child:    rawChild,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	directChild := *rawChild
	directChild.Parent = model.ObjectID{}
	value, access, store := nativeFocusEngine(t, graph, map[model.ObjectID]*model.Node{child: &directChild})
	items := nativeFocusOutput(t, value, access, store, child)
	for _, item := range items {
		if strings.Contains(item.Text, "raw-secret") {
			t.Fatalf("native focus context leaked protected content: %#v", item)
		}
	}
	if speech := focusSpeech(items); !strings.Contains(speech, "[redacted]") {
		t.Fatalf("protected focus was not redacted: %q", speech)
	}
}

func focusSpeech(items []events.Event) string {
	for _, item := range items {
		if item.Kind == events.KindSpeech {
			return item.Text
		}
	}
	return ""
}

func nativeFocusEngine(t *testing.T, graph *model.Graph, directNodes map[model.ObjectID]*model.Node) (*Engine, *fakeAccess, *events.Store) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	access := &fakeAccess{graph: graph, readNodes: directNodes, events: make(chan NativeEvent, 10)}
	store := events.NewStore(500)
	presenter, err := profile.NewPresenter("en-US")
	if err != nil {
		t.Fatal(err)
	}
	value := New(access, store, presenter, braille.Passthrough{}, synth.Silence{SampleRate: 22050}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Locale: "en-US", KeyboardLayout: "desktop"})
	if err := value.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := value.BeginSession("test"); err != nil {
		t.Fatal(err)
	}
	return value, access, store
}

func nativeFocusOutput(t *testing.T, value *Engine, access *fakeAccess, store *events.Store, id model.ObjectID) []events.Event {
	t.Helper()
	before := store.Cursor()
	access.events <- NativeEvent{Name: "org.a11y.atspi.Event.Object.StateChanged", Source: id, Detail: "focused", Detail1: 1}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	items, _, timedOut, err := store.Wait(ctx, before, "test", 5*time.Millisecond)
	if err != nil || timedOut {
		t.Fatalf("waiting for native focus output: timedOut=%v err=%v events=%#v", timedOut, err, items)
	}
	if err := value.WaitForSynthesis(ctx); err != nil {
		t.Fatal(err)
	}
	return items
}
