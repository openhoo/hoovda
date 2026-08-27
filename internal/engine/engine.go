package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/openhoo/hoovda/internal/braille"
	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
	"github.com/openhoo/hoovda/internal/profile"
	"github.com/openhoo/hoovda/internal/synth"
)

type Accessibility interface {
	BrowserGraph(context.Context, string) (*model.Graph, error)
	ReadNode(context.Context, model.ObjectID) (*model.Node, error)
	DoDefaultAction(context.Context, model.ObjectID) error
	Events() <-chan NativeEvent
}

type NativeEvent struct {
	Name    string
	Source  model.ObjectID
	Detail  string
	Detail1 int32
	Detail2 int32
	Value   any
}

type AudioSink interface {
	AddAudio(string, int64, synth.Audio)
}

type Config struct {
	Locale          string
	KeyboardLayout  string
	BrowserProcess  string
	StartupTimeout  time.Duration
	RefreshDebounce time.Duration
	SynthRequest    synth.Request
}

type State struct {
	Ready                  bool           `json:"ready"`
	GraphRevision          uint64         `json:"graphRevision"`
	Cursor                 model.Cursor   `json:"cursor"`
	Focus                  model.ObjectID `json:"focus"`
	BrowserWindowActive    bool           `json:"browserWindowActive"`
	WebContentFocused      bool           `json:"webContentFocused"`
	SingleLetterNavigation bool           `json:"singleLetterNavigation"`
	LastSequence           uint64         `json:"lastSequence"`
}

type Engine struct {
	access    Accessibility
	store     *events.Store
	presenter *profile.Presenter
	braille   braille.Translator
	synth     synth.Driver
	audioSink AudioSink
	logger    *slog.Logger
	cfg       Config

	mu                  sync.RWMutex
	refreshMu           sync.Mutex
	graph               *model.Graph
	graphDirty          bool
	cursor              model.Cursor
	focus               model.ObjectID
	ready               bool
	browserWindowActive bool
	webContentFocused   bool
	singleLetter        bool
	activeSession       string
	synthCancel         context.CancelFunc
	synthDone           <-chan struct{}
}

func New(access Accessibility, store *events.Store, presenter *profile.Presenter, brailleTranslator braille.Translator, synthDriver synth.Driver, sink AudioSink, logger *slog.Logger, cfg Config) *Engine {
	if cfg.RefreshDebounce == 0 {
		cfg.RefreshDebounce = 50 * time.Millisecond
	}
	if cfg.SynthRequest.Rate == 0 {
		cfg.SynthRequest.Rate = 175
	}
	if cfg.SynthRequest.Pitch == 0 {
		cfg.SynthRequest.Pitch = 50
	}
	if cfg.SynthRequest.Volume == 0 {
		cfg.SynthRequest.Volume = 100
	}
	return &Engine{access: access, store: store, presenter: presenter, braille: brailleTranslator, synth: synthDriver, audioSink: sink, logger: logger, cfg: cfg, singleLetter: true, cursor: model.Cursor{Mode: "browse"}}
}

func (e *Engine) Start(ctx context.Context) error {
	refreshCtx := ctx
	cancel := func() {}
	if e.cfg.StartupTimeout > 0 {
		refreshCtx, cancel = context.WithTimeout(ctx, e.cfg.StartupTimeout)
	}
	defer cancel()
	if err := e.Refresh(refreshCtx); err != nil {
		return err
	}
	go e.eventLoop(ctx)
	return nil
}

func (e *Engine) Refresh(ctx context.Context) error {
	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()
	graph, err := e.access.BrowserGraph(ctx, e.cfg.BrowserProcess)
	if err != nil {
		return err
	}
	e.mu.Lock()
	oldGraph := e.graph
	e.graph = graph
	e.graphDirty = false
	if _, ok := graph.Node(e.cursor.Object); !ok {
		e.cursor.Object = remapCursor(oldGraph, graph, e.cursor.Object)
		e.cursor.Offset = 0
	}
	e.ready = true
	e.mu.Unlock()
	return nil
}

func initialCursor(graph *model.Graph) model.ObjectID {
	if graph == nil {
		return model.ObjectID{}
	}
	var document model.ObjectID
	for _, id := range graph.Order {
		if node := graph.Nodes[id]; node != nil && strings.HasPrefix(node.Role, "document") {
			if !document.Valid() || node.HasState("focused") || node.HasState("showing") {
				document = id
			}
		}
	}
	if document.Valid() {
		return document
	}
	if len(graph.Order) > 0 {
		return graph.Order[0]
	}
	return graph.Root
}

func remapCursor(oldGraph, newGraph *model.Graph, oldID model.ObjectID) model.ObjectID {
	if newGraph == nil {
		return model.ObjectID{}
	}
	if oldGraph == nil {
		return initialCursor(newGraph)
	}
	oldNode := oldGraph.Nodes[oldID]
	oldDocumentID, scoped := oldGraph.DocumentRoot(oldID)
	oldDocument := oldGraph.Nodes[oldDocumentID]
	if oldNode == nil || !scoped || oldDocument == nil {
		return initialCursor(newGraph)
	}
	newDocumentID, found := matchingDocument(newGraph, oldDocument)
	if !found {
		return initialCursor(newGraph)
	}
	if oldID == oldDocumentID {
		return newDocumentID
	}
	candidates := make([]model.ObjectID, 0, 1)
	for _, id := range newGraph.Order {
		node := newGraph.Nodes[id]
		if node != nil && newGraph.InDocument(id, newDocumentID) && sameSemanticNode(oldNode, node) {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	return newDocumentID
}

func matchingDocument(graph *model.Graph, old *model.Node) (model.ObjectID, bool) {
	var fallback model.ObjectID
	for _, id := range graph.Order {
		node := graph.Nodes[id]
		if node == nil || !strings.HasPrefix(node.Role, "document") || node.Name != old.Name {
			continue
		}
		fallback = id
		if node.HasState("focused") && node.HasState("showing") {
			fallback = id
		}
	}
	return fallback, fallback.Valid()
}

func sameSemanticNode(a, b *model.Node) bool {
	if a.Role != b.Role || a.Name != b.Name {
		return false
	}
	if a.AccessibleID != "" || b.AccessibleID != "" {
		return a.AccessibleID == b.AccessibleID
	}
	for _, key := range []string{"id", "tag", "xml-roles"} {
		if a.Attributes[key] != b.Attributes[key] {
			return false
		}
	}
	return true
}

func (e *Engine) BeginSession(id string) error {
	if id == "" {
		return errors.New("session id is empty")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.activeSession != "" {
		return errors.New("another test session is active")
	}
	e.activeSession = id
	return nil
}

func (e *Engine) EndSession(id string) {
	e.mu.Lock()
	if e.activeSession == id {
		e.activeSession = ""
	}
	e.mu.Unlock()
}

func (e *Engine) WaitForSynthesis(ctx context.Context) error {
	for {
		e.mu.RLock()
		done := e.synthDone
		e.mu.RUnlock()
		if done == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
		}
		e.mu.RLock()
		current := e.synthDone
		e.mu.RUnlock()
		if current == done {
			return nil
		}
	}
}

func (e *Engine) State() State {
	e.mu.RLock()
	defer e.mu.RUnlock()
	revision := uint64(0)
	if e.graph != nil {
		revision = e.graph.Revision
	}
	return State{Ready: e.ready, GraphRevision: revision, Cursor: e.cursor, Focus: e.focus, BrowserWindowActive: e.browserWindowActive, WebContentFocused: e.webContentFocused, SingleLetterNavigation: e.singleLetter, LastSequence: e.store.Cursor()}
}

func (e *Engine) DocumentSnapshot() []model.Node {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.graph.Snapshot()
}

func (e *Engine) HandleGesture(ctx context.Context, gesture string) (bool, error) {
	command, ok := profile.CommandByGesture(gesture, e.cfg.KeyboardLayout)
	if !ok {
		return false, nil
	}
	consume, err := e.execute(ctx, command)
	return consume, err
}

func (e *Engine) ExecuteDirect(ctx context.Context, commandID string) error {
	command, ok := profile.CommandByID(commandID)
	if !ok {
		return fmt.Errorf("unsupported command %q", commandID)
	}
	_, err := e.execute(ctx, command)
	return err
}

func (e *Engine) execute(ctx context.Context, command profile.Command) (bool, error) {
	e.mu.RLock()
	sessionID := e.activeSession
	mode := e.cursor.Mode
	singleLetter := e.singleLetter
	e.mu.RUnlock()
	e.store.Append(events.Event{Kind: events.KindCommandStarted, SessionID: sessionID, CausalCommand: command.ID, Text: command.Label})
	consume := command.ConsumesBrowse && mode == "browse"
	var err error
	if commandNeedsGraph(command) {
		err = e.refreshIfDirty(ctx)
	}
	if err != nil {
		e.store.Append(events.Event{Kind: events.KindCommandSettled, SessionID: sessionID, CausalCommand: command.ID, Reason: err.Error()})
		return consume, err
	}
	switch command.Category {
	case "quickNavigation":
		if !singleLetter {
			consume = false
			break
		}
		err = e.navigate(command)
	case "text":
		err = e.moveText(command)
	case "activation":
		if consume {
			err = e.activate(ctx)
		}
	case "mode":
		err = e.modeCommand(command)
	case "report":
		err = e.report(command)
	case "table":
		err = e.navigateTable(command)
	case "dialog":
		err = e.reportUnavailable(command)
	case "focus":
		consume = false
	}
	reason := "completed"
	if err != nil {
		reason = err.Error()
	}
	e.store.Append(events.Event{Kind: events.KindCommandSettled, SessionID: sessionID, CausalCommand: command.ID, Reason: reason})
	return consume, err
}

func commandNeedsGraph(command profile.Command) bool {
	switch command.Category {
	case "quickNavigation", "text", "activation", "report", "table":
		return true
	default:
		return false
	}
}

func (e *Engine) refreshIfDirty(ctx context.Context) error {
	e.mu.RLock()
	dirty := e.graphDirty
	e.mu.RUnlock()
	if !dirty {
		return nil
	}
	return e.Refresh(ctx)
}

func (e *Engine) navigate(command profile.Command) error {
	e.mu.Lock()
	graph, current := e.graph, e.cursor.Object
	if graph == nil {
		e.mu.Unlock()
		return errors.New("accessible graph is unavailable")
	}
	node, ok := graph.MoveInDocument(current, command.Direction, profile.MatchTarget(command.Target))
	if ok {
		e.cursor.Object, e.cursor.Offset = node.ID, 0
	}
	e.mu.Unlock()
	if !ok {
		e.emit(e.presenter.NoTarget(command.Target, command.Direction), nil, command.ID, "navigationBoundary", "normal")
		return nil
	}
	e.emit(e.presenter.Present(node, "quickNavigation"), node, command.ID, "quickNavigation", "normal")
	return nil
}

func (e *Engine) moveText(command profile.Command) error {
	e.mu.Lock()
	graph := e.graph
	if graph == nil {
		e.mu.Unlock()
		return errors.New("accessible graph is unavailable")
	}
	node := graph.Nodes[e.cursor.Object]
	if node == nil {
		e.mu.Unlock()
		return errors.New("cursor node is unavailable")
	}
	if command.ID == "documentStart" {
		if document, ok := graph.DocumentRoot(e.cursor.Object); ok {
			e.cursor.Object = document
			e.cursor.Offset = 0
			node = graph.Nodes[document]
		}
	} else if command.ID == "documentEnd" {
		if document, ok := graph.DocumentRoot(e.cursor.Object); ok {
			for index := len(graph.Order) - 1; index >= 0; index-- {
				if graph.InDocument(graph.Order[index], document) {
					e.cursor.Object = graph.Order[index]
					node = graph.Nodes[e.cursor.Object]
					break
				}
			}
			e.cursor.Offset = len([]rune(node.SpokenContent()))
		}
	} else {
		next, ok := graph.MoveInDocument(e.cursor.Object, command.Direction, nil)
		if ok {
			e.cursor.Object, e.cursor.Offset, node = next.ID, 0, next
		}
	}
	e.mu.Unlock()
	e.emit(e.presenter.Present(node, "textNavigation"), node, command.ID, "textNavigation", "normal")
	return nil
}

func (e *Engine) navigateTable(command profile.Command) error {
	e.mu.Lock()
	graph := e.graph
	if graph == nil {
		e.mu.Unlock()
		return errors.New("accessible graph is unavailable")
	}
	current := graph.Nodes[e.cursor.Object]
	if current == nil {
		e.mu.Unlock()
		return errors.New("cursor node is unavailable")
	}
	match := func(node *model.Node) bool {
		if node.Role != "table cell" && node.Role != "cell" && node.Role != "row header" && node.Role != "column header" {
			return false
		}
		if strings.Contains(command.ID, "Column") {
			return node.Row == current.Row && node.Column == current.Column+command.Direction
		}
		return node.Column == current.Column && node.Row == current.Row+command.Direction
	}
	next, ok := graph.MoveInDocument(current.ID, command.Direction, match)
	if ok {
		e.cursor.Object, e.cursor.Offset = next.ID, 0
	}
	e.mu.Unlock()
	if !ok {
		e.emit(e.presenter.NoTarget("table cell", command.Direction), nil, command.ID, "tableBoundary", "normal")
		return nil
	}
	e.emit(e.presenter.Present(next, "tableNavigation"), next, command.ID, "tableNavigation", "normal")
	return nil
}

func (e *Engine) activate(ctx context.Context) error {
	e.mu.RLock()
	id := e.cursor.Object
	e.mu.RUnlock()
	return e.access.DoDefaultAction(ctx, id)
}

func (e *Engine) modeCommand(command profile.Command) error {
	e.mu.Lock()
	if command.ID == "toggleSingleLetterNavigation" {
		e.singleLetter = !e.singleLetter
		value := e.singleLetter
		e.mu.Unlock()
		text := "single letter navigation off"
		if value {
			text = "single letter navigation on"
		}
		e.emit(profile.Presentation{Speech: text, Braille: text}, nil, command.ID, "mode", "normal")
		return nil
	}
	if command.ID == "escape" {
		e.cursor.Mode = "browse"
	} else if command.ID == "toggleFocusMode" {
		if e.cursor.Mode == "browse" {
			e.cursor.Mode = "focus"
		} else {
			e.cursor.Mode = "browse"
		}
	}
	mode := e.cursor.Mode
	e.mu.Unlock()
	e.store.Append(events.Event{Kind: events.KindMode, SessionID: e.session(), CausalCommand: command.ID, Mode: mode})
	e.emit(e.presenter.Mode(mode), nil, command.ID, "mode", "normal")
	return nil
}

func (e *Engine) report(command profile.Command) error {
	e.mu.RLock()
	node := e.graph.Nodes[e.cursor.Object]
	e.mu.RUnlock()
	if node == nil {
		return errors.New("cursor node is unavailable")
	}
	if command.ID != "sayAll" {
		e.emit(e.presenter.Present(node, command.ID), node, command.ID, "report", "normal")
		return nil
	}
	e.mu.RLock()
	graph, start := e.graph, e.graph.Index(node.ID)
	document, scoped := e.graph.DocumentRoot(node.ID)
	e.mu.RUnlock()
	items := []*model.Node{node}
	if graph != nil {
		for index := start + 1; index < len(graph.Order); index++ {
			if scoped && !graph.InDocument(graph.Order[index], document) {
				continue
			}
			items = append(items, graph.Nodes[graph.Order[index]])
		}
	}
	spoken := make([]string, 0, len(items))
	var firstSpeech events.Event
	for _, item := range items {
		presentation := e.presenter.Present(item, "sayAll")
		event := e.emitEvidence(presentation, item, command.ID, "sayAll", "normal", false)
		if firstSpeech.Sequence == 0 && event.Sequence != 0 {
			firstSpeech = event
		}
		if presentation.Speech != "" {
			spoken = append(spoken, presentation.Speech)
		}
	}
	if firstSpeech.Sequence != 0 && len(spoken) > 0 {
		e.startSynthesis(firstSpeech, profile.Presentation{Speech: strings.Join(spoken, ". ")})
	}
	return nil
}

func (e *Engine) reportUnavailable(command profile.Command) error {
	text := command.Label + " is not available in this build"
	e.emit(profile.Presentation{Speech: text, Braille: text}, nil, command.ID, "unsupported", "normal")
	return errors.New(text)
}

func (e *Engine) emit(presentation profile.Presentation, node *model.Node, command, reason, priority string) {
	e.emitEvidence(presentation, node, command, reason, priority, true)
}

func (e *Engine) emitEvidence(presentation profile.Presentation, node *model.Node, command, reason, priority string, synthesize bool) events.Event {
	if presentation.Speech == "" && presentation.Braille == "" {
		return events.Event{}
	}
	sessionID := e.session()
	var source *model.ObjectID
	if node != nil {
		id := node.ID
		source = &id
	}
	speechEvent := e.store.Append(events.Event{Kind: events.KindSpeech, SessionID: sessionID, CausalCommand: command, Source: source, Text: presentation.Speech, SpeechCommands: presentation.SpeechCommands, Reason: reason, Priority: priority})
	translationContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	translation, err := e.braille.Translate(translationContext, presentation.Braille, e.cfg.Locale, 0)
	cancel()
	if err != nil {
		e.logger.Error("braille translation failed", "error", err)
		translation = braille.Result{Text: presentation.Braille, Cells: []byte(presentation.Braille)}
	}
	e.store.Append(events.Event{Kind: events.KindBraille, SessionID: sessionID, CausalCommand: command, Source: source, Text: translation.Text, BrailleCells: translation.Cells, BrailleCursor: translation.Cursor, Reason: reason})
	if synthesize && presentation.Speech != "" {
		e.startSynthesis(speechEvent, presentation)
	}
	return speechEvent
}

func (e *Engine) startSynthesis(event events.Event, presentation profile.Presentation) {
	e.mu.Lock()
	if e.synthCancel != nil {
		e.synthCancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	done := make(chan struct{})
	e.synthCancel = cancel
	e.synthDone = done
	e.mu.Unlock()
	request := e.cfg.SynthRequest
	request.Text, request.Locale, request.Commands = presentation.Speech, e.cfg.Locale, presentation.SpeechCommands
	go func() {
		defer close(done)
		defer cancel()
		audio, err := e.synth.Synthesize(ctx, request)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				e.logger.Error("speech synthesis failed", "error", err)
			}
			return
		}
		if e.audioSink != nil {
			e.audioSink.AddAudio(event.SessionID, event.MonotonicNS, audio)
		}
		e.store.Append(events.Event{Kind: events.KindAudio, SessionID: event.SessionID, CausalCommand: event.CausalCommand, Source: event.Source, AudioOffsetNS: event.MonotonicNS, AudioDurationNS: audio.Duration.Nanoseconds(), Reason: "synthesized"})
	}()
}

func (e *Engine) eventLoop(ctx context.Context) {
	var refreshTimer *time.Timer
	for {
		select {
		case <-ctx.Done():
			return
		case native, ok := <-e.access.Events():
			if !ok {
				return
			}
			if strings.HasSuffix(native.Name, ".Focus") || (strings.HasSuffix(native.Name, ".StateChanged") && native.Detail == "focused" && native.Detail1 != 0) {
				if !e.handleFocus(ctx, native.Source) {
					continue
				}
			}
			if strings.HasSuffix(native.Name, ".Announcement") {
				text, _ := native.Value.(string)
				if text != "" {
					e.emitLiveRegion(profile.Presentation{Speech: text, Braille: text}, nil, livePriority(native.Detail1))
				}
				continue
			}
			if strings.HasSuffix(native.Name, ".TextChanged") && native.Detail == "insert" {
				text, _ := native.Value.(string)
				if text != "" {
					node, err := e.access.ReadNode(ctx, native.Source)
					if err == nil {
						if priority, live := e.liveRegionPriority(node); live {
							e.emitLiveRegion(profile.Presentation{Speech: text, Braille: text}, node, priority)
						}
					}
				}
			}
			if refreshTimer != nil {
				refreshTimer.Stop()
			}
			refreshTimer = time.AfterFunc(e.cfg.RefreshDebounce, func() {
				refreshCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := e.Refresh(refreshCtx); err != nil {
					e.logger.Warn("refresh accessible graph", "error", err)
				}
			})
		}
	}
}

func (e *Engine) liveRegionPriority(node *model.Node) (string, bool) {
	e.mu.RLock()
	graph := e.graph
	e.mu.RUnlock()
	for steps := 0; node != nil && steps < 512; steps++ {
		role := strings.ToLower(node.Role)
		if role == "alert" {
			return "assertive", true
		}
		if role == "statusbar" || role == "status" || role == "log" {
			return "polite", true
		}
		for _, key := range []string{"live", "container-live"} {
			switch strings.ToLower(node.Attributes[key]) {
			case "assertive":
				return "assertive", true
			case "polite":
				return "polite", true
			}
		}
		if graph == nil || !node.Parent.Valid() {
			break
		}
		node = graph.Nodes[node.Parent]
	}
	return "", false
}

func (e *Engine) emitLiveRegion(presentation profile.Presentation, node *model.Node, priority string) {
	var source *model.ObjectID
	if node != nil {
		id := node.ID
		source = &id
	}
	e.store.Append(events.Event{Kind: events.KindLiveRegion, SessionID: e.session(), CausalCommand: "event", Source: source, Text: presentation.Speech, Reason: "liveRegion", Priority: priority})
	e.emit(presentation, node, "event", "liveRegion", priority)
}

func (e *Engine) handleFocus(ctx context.Context, id model.ObjectID) bool {
	node, err := e.access.ReadNode(ctx, id)
	if err != nil {
		e.logger.Debug("read focus object", "error", err)
		return false
	}
	isDocument := node.Role == "document web" || node.Role == "document frame"
	e.mu.Lock()
	e.focus = id
	e.browserWindowActive = true
	e.webContentFocused = isDocument || hasDocumentAncestor(e.graph, id)
	if isDocument {
		e.graphDirty = true
	}
	if isDocument || e.cursor.Mode == "focus" || node.HasState("focusable") {
		e.cursor.Object, e.cursor.Offset = id, 0
	}
	e.mu.Unlock()
	e.store.Append(events.Event{Kind: events.KindFocus, SessionID: e.session(), Source: &id, Text: node.Name, Reason: "nativeFocus"})
	e.emit(e.presenter.Present(node, "focus"), node, "focus", "nativeFocus", "normal")
	return isDocument
}

func hasDocumentAncestor(graph *model.Graph, id model.ObjectID) bool {
	for steps := 0; graph != nil && steps < 512; steps++ {
		node := graph.Nodes[id]
		if node == nil {
			return false
		}
		if node.Role == "document web" || node.Role == "document frame" {
			return true
		}
		if !node.Parent.Valid() {
			return false
		}
		id = node.Parent
	}
	return false
}

func livePriority(value int32) string {
	if value >= 2 {
		return "assertive"
	}
	return "polite"
}
func (e *Engine) session() string { e.mu.RLock(); defer e.mu.RUnlock(); return e.activeSession }
