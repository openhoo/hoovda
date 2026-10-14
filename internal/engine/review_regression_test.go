package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openhoo/hoovda/internal/braille"
	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
	"github.com/openhoo/hoovda/internal/profile"
	"github.com/openhoo/hoovda/internal/synth"
)

type blockedNativeRead struct {
	*fakeAccess
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type pausedNativeGateContext struct {
	context.Context
	waiting chan struct{}
	resume  chan struct{}
	once    sync.Once
}

func (c *pausedNativeGateContext) Done() <-chan struct{} {
	c.once.Do(func() {
		close(c.waiting)
		<-c.resume
	})
	return c.Context.Done()
}

func TestQueuedNativeEventDoesNotEnterNextSession(t *testing.T) {
	value, store := fixtureEngine(t)
	ctx := &pausedNativeGateContext{Context: context.Background(), waiting: make(chan struct{}), resume: make(chan struct{})}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		value.handleNativeEvent(ctx, NativeEvent{Name: "org.a11y.atspi.Event.Focus", Source: model.ObjectID{Bus: "app", Path: "/button"}})
	}()
	select {
	case <-ctx.waiting:
	case <-time.After(time.Second):
		t.Fatal("native handler did not reach its gate wait")
	}
	if err := value.EndSessionContext(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if err := value.BeginSession("next"); err != nil {
		t.Fatal(err)
	}
	before := store.Cursor()
	close(ctx.resume)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("queued native event did not finish")
	}
	if output, _, err := store.Snapshot(before, "next"); err != nil || len(output) != 0 {
		t.Fatalf("old native event entered new session: events=%#v error=%v", output, err)
	}
}

func TestLateSameDocumentFocusPreservesTableBrowsePosition(t *testing.T) {
	app := model.ObjectID{Bus: "app", Path: "/app"}
	document := model.ObjectID{Bus: "app", Path: "/document"}
	other := model.ObjectID{Bus: "app", Path: "/other-document"}
	chrome := model.ObjectID{Bus: "app", Path: "/address-bar"}
	table := model.ObjectID{Bus: "app", Path: "/table"}
	first := model.ObjectID{Bus: "app", Path: "/table/first"}
	second := model.ObjectID{Bus: "app", Path: "/table/second"}
	graph, err := model.NewGraph(app, map[model.ObjectID]*model.Node{
		app:      {ID: app, Role: "application", Children: []model.ObjectID{document, other, chrome}},
		document: {ID: document, Parent: app, Role: "document web", Name: "Table navigation oracle", States: map[string]bool{"showing": true, "focused": true}, Children: []model.ObjectID{table}},
		other:    {ID: other, Parent: app, Role: "document web", Name: "Different document"},
		chrome:   {ID: chrome, Parent: app, Role: "entry", Name: "Address and search bar", States: map[string]bool{"focusable": true}},
		table:    {ID: table, Parent: document, Role: "table", RowCount: 2, ColumnCount: 1, Children: []model.ObjectID{first, second}},
		first:    {ID: first, Parent: table, Role: "table cell", Name: "First", Row: 1, Column: 1, Table: table},
		second:   {ID: second, Parent: table, Role: "table cell", Name: "Second", Row: 2, Column: 1, Table: table},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := engineForGraph(t, graph)
	if err := value.ExecuteDirect(context.Background(), "nextTable"); err != nil {
		t.Fatal(err)
	}
	value.mu.Lock()
	value.cursor.Offset = 3
	value.mu.Unlock()
	value.handleFocus(context.Background(), chrome)
	value.handleFocus(context.Background(), document)
	if got := value.State().Cursor; got.Object != first || got.Offset != 3 {
		t.Fatalf("same-document native focus reset logical table position: %#v", got)
	}
	if err := value.ExecuteDirect(context.Background(), "nextTableRow"); err != nil {
		t.Fatalf("late same-document focus broke table navigation: %v", err)
	}
	if got := value.State().Cursor.Object; got != second {
		t.Fatalf("next table row = %v, want %v", got, second)
	}
	value.handleFocus(context.Background(), other)
	if got := value.State().Cursor; got.Object != other || got.Offset != 0 {
		t.Fatalf("new document focus failed to relocate logical cursor: %#v", got)
	}
}

func TestFindPreviousDoesNotRepeatNormalizedCurrentTextOccurrence(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	item := model.ObjectID{Bus: "app", Path: "/item"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root: {ID: root, Role: "document web", Children: []model.ObjectID{item}},
		item: {ID: item, Parent: root, Role: "list item", Text: "• Accessibility toolkit"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	value, store := engineForGraph(t, graph)
	if err := value.ExecuteDirectWithArgument(context.Background(), "find", "Accessibility toolkit"); err != nil {
		t.Fatal(err)
	}
	if err := value.ExecuteDirect(context.Background(), "findNext"); err != nil {
		t.Fatal(err)
	}
	before := store.Cursor()
	if err := value.ExecuteDirect(context.Background(), "findPrevious"); err != nil {
		t.Fatal(err)
	}
	if got := speechAfter(t, store, before); got != "no previous text Accessibility toolkit" {
		t.Fatalf("findPrevious repeated normalized current text: %q", got)
	}
}

func (a *blockedNativeRead) ReadNode(ctx context.Context, id model.ObjectID) (*model.Node, error) {
	a.once.Do(func() { close(a.started) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-a.release:
		return a.fakeAccess.ReadNode(ctx, id)
	}
}

func TestSessionFinalizationWaitsForInFlightNativeRead(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		name := "seal"
		if cleanup {
			name = "cleanup"
		}
		t.Run(name, func(t *testing.T) {
			root := model.ObjectID{Bus: "app", Path: "/root"}
			button := model.ObjectID{Bus: "app", Path: "/button"}
			graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
				root:   {ID: root, Role: "document web", Children: []model.ObjectID{button}},
				button: {ID: button, Parent: root, Role: "push button", Name: "Native output before finish", States: map[string]bool{"focusable": true}},
			}, 1)
			if err != nil {
				t.Fatal(err)
			}
			access := &blockedNativeRead{fakeAccess: &fakeAccess{graph: graph, events: make(chan NativeEvent, 1)}, started: make(chan struct{}), release: make(chan struct{})}
			store := events.NewStore(100)
			presenter, _ := profile.NewPresenter("en-US")
			value := New(access, store, presenter, braille.Passthrough{}, synth.Silence{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Locale: "en-US", KeyboardLayout: "desktop"})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			defer close(access.release)
			if err := value.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if err := value.BeginSession("native-read"); err != nil {
				t.Fatal(err)
			}
			access.events <- NativeEvent{Name: "org.a11y.atspi.Event.Focus", Source: button}
			select {
			case <-access.started:
			case <-time.After(time.Second):
				t.Fatal("native read did not begin")
			}
			finishCtx, finishCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer finishCancel()
			if cleanup {
				err = value.EndSessionContext(finishCtx, "native-read")
			} else {
				err = value.SealSessionOutputsContext(finishCtx, "native-read")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s finalized while native read was still running: %v", name, err)
			}
			value.mu.RLock()
			active, sealed := value.activeSession, value.outputsSealed
			value.mu.RUnlock()
			if active != "native-read" || sealed {
				t.Fatalf("timed-out finalization discarded native event attribution: session=%q sealed=%t", active, sealed)
			}
		})
	}
}

func TestDocumentSelectionKeepsActiveDocumentAheadOfBackgroundMatches(t *testing.T) {
	app := model.ObjectID{Bus: "app", Path: "/app"}
	active := model.ObjectID{Bus: "app", Path: "/active"}
	background := model.ObjectID{Bus: "app", Path: "/background"}
	graph, err := model.NewGraph(app, map[model.ObjectID]*model.Node{
		app:        {ID: app, Role: "application", Children: []model.ObjectID{active, background}},
		active:     {ID: active, Parent: app, Role: "document web", Name: "Same title", States: map[string]bool{"focused": true, "showing": true}},
		background: {ID: background, Parent: app, Role: "document web", Name: "Same title", States: map[string]bool{"showing": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := initialCursor(graph); got != active {
		t.Errorf("initial document = %v, want active %v", got, active)
	}
	if got, ok := matchingDocument(graph, graph.Nodes[active]); !ok || got != active {
		t.Errorf("matching document = %v (%v), want active %v", got, ok, active)
	}
}

func TestReviewLineNavigationAdvancesThroughEmptyLines(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	text := model.ObjectID{Bus: "app", Path: "/text"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root: {ID: root, Role: "document web", Children: []model.ObjectID{text}},
		text: {ID: text, Parent: root, Role: "static", Text: "first\n\nthird"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	engine.review = model.Cursor{Object: text, Mode: "object"}
	for _, want := range []struct {
		offset int
		speech string
	}{{6, "blank"}, {7, "third"}} {
		before := store.Cursor()
		if err := engine.ExecuteDirect(context.Background(), "reviewNextLine"); err != nil {
			t.Fatal(err)
		}
		if got := engine.State().Review.Offset; got != want.offset {
			t.Errorf("review offset = %d, want %d", got, want.offset)
		}
		if got := speechAfter(t, store, before); got != want.speech {
			t.Errorf("review speech = %q, want %q", got, want.speech)
		}
	}
}

func TestFindNavigatesUnicodeOccurrencesWithinCurrentTextObject(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	text := model.ObjectID{Bus: "app", Path: "/text"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root: {ID: root, Role: "document web", Children: []model.ObjectID{text}},
		text: {ID: text, Parent: root, Role: "static", Text: "🙂 needle and NEEDLE"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := engineForGraph(t, graph)
	engine.cursor.Object = text
	for _, step := range []struct {
		command string
		offset  int
	}{{"find", 2}, {"findNext", 13}, {"findPrevious", 2}} {
		if err := engine.ExecuteDirectWithArgument(context.Background(), step.command, "needle"); err != nil {
			t.Fatal(err)
		}
		if got := engine.State().Cursor; got.Object != text || got.Offset != step.offset {
			t.Errorf("%s cursor = %#v, want %v at %d", step.command, got, text, step.offset)
		}
	}
}

func TestAtomicLiveRegionReadsDocumentOrderAndKeepsRepeatedSiblings(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	status := model.ObjectID{Bus: "app", Path: "/status"}
	group := model.ObjectID{Bus: "app", Path: "/status/group"}
	first := model.ObjectID{Bus: "app", Path: "/status/group/first"}
	second := model.ObjectID{Bus: "app", Path: "/status/second"}
	third := model.ObjectID{Bus: "app", Path: "/status/third"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:   {ID: root, Role: "document web", Children: []model.ObjectID{status}},
		status: {ID: status, Parent: root, Role: "status bar", Attributes: map[string]string{"atomic": "true"}, Children: []model.ObjectID{group, second, third}},
		group:  {ID: group, Parent: status, Role: "panel", Children: []model.ObjectID{first}},
		first:  {ID: first, Parent: group, Role: "static", Text: "first"},
		second: {ID: second, Parent: status, Role: "static", Text: "repeat"},
		third:  {ID: third, Parent: status, Role: "static", Text: "repeat"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	before := store.Cursor()
	engine.handleLiveTextChange(context.Background(), NativeEvent{Source: status, Detail: "insert", Value: "repeat"})
	if got := speechAfter(t, store, before); got != "first repeat repeat" {
		t.Fatalf("atomic status speech = %q, want document order with repeated siblings", got)
	}
}

func TestReviewCopyDoesNotClaimRejectedNativeSelection(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	text := model.ObjectID{Bus: "app", Path: "/text"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root: {ID: root, Role: "document web", Children: []model.ObjectID{text}},
		text: {ID: text, Parent: root, Role: "static", Text: "abcdefghij"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, _ := engineForGraph(t, graph)
	engine.review = model.Cursor{Object: text, Offset: 2, Mode: "object"}
	if err := engine.ExecuteDirect(context.Background(), "setReviewCopyStart"); err != nil {
		t.Fatal(err)
	}
	engine.review.Offset = 5
	engine.access.(*fakeAccess).selectionErr = errors.New("native selection rejected")
	if err := engine.ExecuteDirect(context.Background(), "copyToReviewPosition"); err == nil {
		t.Fatal("native selection rejection did not fail the command")
	}
	if selection := engine.State().ReviewSelection; selection != nil {
		t.Fatalf("failed native selection was reported as saved: %#v", selection)
	}
}

func TestAtomicLiveRegionDoesNotReadProtectedDescendants(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	status := model.ObjectID{Bus: "app", Path: "/status"}
	password := model.ObjectID{Bus: "app", Path: "/status/password"}
	child := model.ObjectID{Bus: "app", Path: "/status/password/child"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:     {ID: root, Role: "document web", Children: []model.ObjectID{status}},
		status:   {ID: status, Parent: root, Role: "status bar", Attributes: map[string]string{"atomic": "true"}, Children: []model.ObjectID{password}},
		password: {ID: password, Parent: status, Role: "password text", Name: "Password", Children: []model.ObjectID{child}},
		child:    {ID: child, Parent: password, Role: "static", Text: "protected-raw-content"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	before := store.Cursor()
	engine.handleLiveTextChange(context.Background(), NativeEvent{Source: status, Detail: "insert", Value: "updated"})
	if got := speechAfter(t, store, before); got != "[redacted]" {
		t.Fatalf("protected-descendant status speech = %q", got)
	}
}

func TestNonAtomicLiveRegionRedactsProtectedNativeValue(t *testing.T) {
	for _, test := range []struct {
		name         string
		cached       bool
		parentless   bool
		announcement bool
	}{
		{name: "cached", cached: true},
		{name: "uncached"},
		{name: "uncached-parentless", parentless: true},
		{name: "uncached-announcement", parentless: true, announcement: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := model.ObjectID{Bus: "app", Path: "/root"}
			status := model.ObjectID{Bus: "app", Path: "/status"}
			password := model.ObjectID{Bus: "app", Path: "/status/password"}
			child := model.ObjectID{Bus: "app", Path: "/status/password/child"}
			raw := &model.Node{ID: child, Parent: password, Role: "static", Name: "protected-native-value", Text: "protected-native-value"}
			if test.parentless {
				raw.Parent = model.ObjectID{}
				raw.Attributes = map[string]string{"container-live": "polite"}
			}
			nodes := map[model.ObjectID]*model.Node{
				root:     {ID: root, Role: "document web", Children: []model.ObjectID{status}},
				status:   {ID: status, Parent: root, Role: "status bar", Children: []model.ObjectID{password}},
				password: {ID: password, Parent: status, Role: "password text", Name: "Password", Children: []model.ObjectID{child}},
			}
			if test.cached {
				nodes[child] = raw
			}
			graph, err := model.NewGraph(root, nodes, 1)
			if err != nil {
				t.Fatal(err)
			}
			value, store := engineForGraph(t, graph)
			value.access.(*fakeAccess).readNodes = map[model.ObjectID]*model.Node{child: raw}
			before := store.Cursor()
			if test.announcement {
				value.handleNativeEvent(context.Background(), NativeEvent{Name: "org.a11y.atspi.Event.Object.Announcement", Source: child, Value: "protected-native-value"})
			} else {
				value.handleLiveTextChange(context.Background(), NativeEvent{Source: child, Detail: "insert", Value: "protected-native-value"})
			}
			if got := speechAfter(t, store, before); got != "[redacted]" {
				t.Fatalf("non-atomic protected native.Value leaked: %q", got)
			}
		})
	}
}

func TestNativeFocusRedactsProtectedDescendantContent(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	password := model.ObjectID{Bus: "app", Path: "/password"}
	child := model.ObjectID{Bus: "app", Path: "/password/child"}
	raw := &model.Node{ID: child, Parent: password, Role: "static", Name: "raw-password-content", Text: "raw-password-content"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:     {ID: root, Role: "document web", Children: []model.ObjectID{password}},
		password: {ID: password, Parent: root, Role: "password text", Name: "Password", Children: []model.ObjectID{child}},
		child:    raw,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	engine.access.(*fakeAccess).readNodes = map[model.ObjectID]*model.Node{child: raw}
	before := store.Cursor()
	engine.handleFocus(context.Background(), child)
	got, _, err := store.Snapshot(before, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range got {
		if event.Text == "raw-password-content" {
			t.Fatalf("native focus exposed protected descendant: %#v", event)
		}
	}
	if got := speechAfter(t, store, before); !strings.Contains(got, "[redacted]") {
		t.Fatalf("native focus speech = %q", got)
	}
}

func TestReviewCurrentLineKeepsTrailingEmptyLine(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	text := model.ObjectID{Bus: "app", Path: "/text"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root: {ID: root, Role: "document web", Children: []model.ObjectID{text}},
		text: {ID: text, Parent: root, Role: "static", Text: "first\n"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	engine, store := engineForGraph(t, graph)
	engine.review = model.Cursor{Object: text, Mode: "object"}
	if err := engine.ExecuteDirect(context.Background(), "reviewNextLine"); err != nil {
		t.Fatal(err)
	}
	before := store.Cursor()
	if err := engine.ExecuteDirect(context.Background(), "reviewCurrentLine"); err != nil {
		t.Fatalf("reviewing trailing blank line: %v", err)
	}
	if got := speechAfter(t, store, before); got != "blank" {
		t.Fatalf("current trailing line = %q", got)
	}
}

type gatedSynthesis struct {
	started chan string
	release chan struct{}
}

func (s gatedSynthesis) Name() string                   { return "gated-test" }
func (s gatedSynthesis) Version(context.Context) string { return "1" }
func (s gatedSynthesis) Synthesize(ctx context.Context, request synth.Request) (synth.Audio, error) {
	s.started <- request.Text
	if request.Text == "first" {
		// Model a driver finishing successfully while a replacement utterance
		// cancels it. Its completion and PCM append can lag the newer request.
		<-s.release
	}
	return (synth.Silence{}).Synthesize(ctx, request)
}

func TestSealSessionDrainsReplacedSynthesisAndSuppressesLateOutputs(t *testing.T) {
	engine, store := fixtureEngine(t)
	driver := gatedSynthesis{started: make(chan string, 4), release: make(chan struct{})}
	engine.synth = driver
	engine.emit(profile.Presentation{Speech: "first"}, nil, "readCurrent", "report", "normal")
	if got := <-driver.started; got != "first" {
		t.Fatalf("first synthesis = %q", got)
	}
	engine.emit(profile.Presentation{Speech: "second"}, nil, "readCurrent", "report", "normal")
	if got := <-driver.started; got != "second" {
		t.Fatalf("replacement synthesis = %q", got)
	}
	if err := engine.SealSessionOutputs("test"); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := engine.WaitForSynthesis(short)
	cancel()
	close(driver.release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pending previous synthesis was not drained: %v", err)
	}
	wait, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := engine.WaitForSynthesis(wait); err != nil {
		t.Fatal(err)
	}
	evidence, _, err := store.Snapshot(0, "test")
	if err != nil {
		t.Fatal(err)
	}
	audioCount := 0
	for _, event := range evidence {
		if event.Kind == events.KindAudio {
			audioCount++
		}
	}
	if audioCount != 2 {
		t.Fatalf("drained audio count = %d, want both scheduled utterances", audioCount)
	}
	before := store.Cursor()
	engine.emit(profile.Presentation{Speech: "late"}, nil, "event", "nativeFocus", "normal")
	engine.handleFocus(context.Background(), model.ObjectID{Bus: "app", Path: "/button"})
	if got := store.Cursor(); got != before {
		t.Fatalf("sealed session published later output: cursor %d -> %d", before, got)
	}
	engine.EndSession("test")
	if err := engine.BeginSession("next"); err != nil {
		t.Fatal(err)
	}
	engine.emit(profile.Presentation{Speech: "reopened"}, nil, "readCurrent", "report", "normal")
	if got := <-driver.started; got != "reopened" {
		t.Fatalf("new session synthesis = %q", got)
	}
	if err := engine.WaitForSynthesis(wait); err != nil {
		t.Fatal(err)
	}
}

type unavailableBraille struct {
	braille.Passthrough
}

func (unavailableBraille) Translate(context.Context, string, string, int) (braille.Result, error) {
	return braille.Result{}, errors.New("braille helper unavailable")
}

func TestUnavailableBraillePreservesLogicalTextWithoutInventedDotCells(t *testing.T) {
	engine, store := fixtureEngine(t)
	engine.braille = unavailableBraille{}
	before := store.Cursor()
	engine.emit(profile.Presentation{Braille: "logical braille"}, nil, "readCurrent", "report", "normal")
	evidence, _, err := store.Snapshot(before, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range evidence {
		if event.Kind == events.KindBraille {
			if event.Text != "logical braille" || len(event.BrailleCells) != 0 {
				t.Fatalf("translation failure fabricated physical braille evidence: %#v", event)
			}
			return
		}
	}
	t.Fatal("logical braille event missing")
}

type gatedBraille struct {
	braille.Passthrough
	entered chan struct{}
	release chan struct{}
}

func (b gatedBraille) Translate(ctx context.Context, text, locale string, cursor int) (braille.Result, error) {
	close(b.entered)
	select {
	case <-b.release:
		return b.Passthrough.Translate(ctx, text, locale, cursor)
	case <-ctx.Done():
		return braille.Result{}, ctx.Err()
	}
}

func TestSessionFinalizationCancelsWhileNativeOutputIsBusy(t *testing.T) {
	for _, operation := range []string{"seal", "end"} {
		t.Run(operation, func(t *testing.T) {
			engine, _ := fixtureEngine(t)
			translator := gatedBraille{entered: make(chan struct{}), release: make(chan struct{})}
			engine.braille = translator
			emitted := make(chan struct{})
			go func() {
				engine.emit(profile.Presentation{Braille: "native output in progress"}, nil, "event", "nativeFocus", "normal")
				close(emitted)
			}()
			<-translator.entered
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			result := make(chan error, 1)
			go func() {
				if operation == "seal" {
					result <- engine.SealSessionOutputsContext(ctx, "test")
				} else {
					result <- engine.EndSessionContext(ctx, "test")
				}
			}()
			select {
			case err := <-result:
				close(translator.release)
				<-emitted
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("busy-output cancellation = %v", err)
				}
			case <-time.After(150 * time.Millisecond):
				close(translator.release)
				<-emitted
				<-result
				t.Fatal("native output lock ignored finalization deadline")
			}
			engine.mu.RLock()
			active, sealed := engine.activeSession, engine.outputsSealed
			engine.mu.RUnlock()
			if active != "test" || sealed {
				t.Fatalf("canceled finalization did not preserve retry state: active=%q sealed=%v", active, sealed)
			}
			// Cancellation must release the command gate as well as leaving the
			// active session intact. A later retry must acquire both gates.
			if err := engine.SealSessionOutputsContext(context.Background(), "test"); err != nil {
				t.Fatalf("retry seal: %v", err)
			}
			if err := engine.EndSessionContext(context.Background(), "test"); err != nil {
				t.Fatalf("retry cleanup: %v", err)
			}
		})
	}
}

func TestCanceledCommandDoesNotPublishOutput(t *testing.T) {
	engine, store := fixtureEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := store.Cursor()
	if err := engine.ExecuteDirect(ctx, "nextHeading"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled command = %v, want cancellation", err)
	}
	if got := store.Cursor(); got != before {
		t.Fatalf("canceled command published output: cursor %d -> %d", before, got)
	}
}
