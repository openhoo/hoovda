package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/openhoo/hoovda/internal/braille"
	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
	"github.com/openhoo/hoovda/internal/profile"
	"github.com/openhoo/hoovda/internal/synth"
)

type Accessibility interface {
	BrowserGraph(context.Context, string, model.ObjectID) (*model.Graph, error)
	ReadNode(context.Context, model.ObjectID) (*model.Node, error)
	DoDefaultAction(context.Context, model.ObjectID) error
	GrabFocus(context.Context, model.ObjectID) error
	SetTextSelection(context.Context, model.ObjectID, int, int) error
	GenerateMouseEvent(context.Context, int, int, string) error
	Events() <-chan NativeEvent
}

type NativeEvent struct {
	Name        string
	Source      model.ObjectID
	Detail      string
	Detail1     int32
	Detail2     int32
	Value       any
	ValueObject model.ObjectID
}

type AudioSink interface {
	AddAudio(string, int64, synth.Audio)
}

type Config struct {
	Locale         string
	KeyboardLayout string
	BrowserProcess string
	StartupTimeout time.Duration
	SynthRequest   synth.Request
}

type State struct {
	Ready                  bool             `json:"ready"`
	GraphRevision          uint64           `json:"graphRevision"`
	Cursor                 model.Cursor     `json:"cursor"`
	Navigator              model.ObjectID   `json:"navigator"`
	Review                 model.Cursor     `json:"review"`
	ReviewCopyStart        *model.Cursor    `json:"reviewCopyStart,omitempty"`
	ReviewSelection        *model.TextRange `json:"reviewSelection,omitempty"`
	CursorInDocument       bool             `json:"cursorInDocument"`
	Focus                  model.ObjectID   `json:"focus"`
	BrowserWindowActive    bool             `json:"browserWindowActive"`
	WebContentFocused      bool             `json:"webContentFocused"`
	SingleLetterNavigation bool             `json:"singleLetterNavigation"`
	NativeSelectionMode    bool             `json:"nativeSelectionMode"`
	MouseX                 int              `json:"mouseX"`
	MouseY                 int              `json:"mouseY"`
	MousePositionKnown     bool             `json:"mousePositionKnown"`
	LeftMouseLocked        bool             `json:"leftMouseLocked"`
	RightMouseLocked       bool             `json:"rightMouseLocked"`
	SpeechMode             string           `json:"speechMode"`
	SpeechPaused           bool             `json:"speechPaused"`
	BrailleOffset          int              `json:"brailleOffset"`
	BrailleTether          string           `json:"brailleTether"`
	LastSequence           uint64           `json:"lastSequence"`
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

	mu                      sync.RWMutex
	refreshMu               sync.Mutex
	graph                   *model.Graph
	graphDirty              bool
	cursor                  model.Cursor
	navigator               model.ObjectID
	review                  model.Cursor
	reviewCopyStart         model.Cursor
	reviewCopySet           bool
	reviewSelection         model.TextRange
	reviewSelectionSet      bool
	focus                   model.ObjectID
	ready                   bool
	browserWindowActive     bool
	webContentFocused       bool
	singleLetter            bool
	nativeSelection         bool
	mouseX                  int
	mouseY                  int
	mousePositionKnown      bool
	leftMouseLocked         bool
	rightMouseLocked        bool
	speechMode              string
	speechPaused            bool
	brailleOffset           int
	brailleTether           string
	activeSession           string
	presentationBaseline    profile.PresentationSettings
	presentationBaselineSet bool
	activeAction            string
	recentLiveRegions       map[liveRegionAnnouncement]time.Time
	liveRegionKnown         map[model.ObjectID]bool
	findQuery               string
	synthCancel             context.CancelFunc
	synthDone               <-chan struct{}
}

type liveRegionAnnouncement struct {
	source   model.ObjectID
	text     string
	priority string
}

func New(access Accessibility, store *events.Store, presenter *profile.Presenter, brailleTranslator braille.Translator, synthDriver synth.Driver, sink AudioSink, logger *slog.Logger, cfg Config) *Engine {
	if cfg.SynthRequest.Rate == 0 {
		cfg.SynthRequest.Rate = 175
	}
	if cfg.SynthRequest.Pitch == 0 {
		cfg.SynthRequest.Pitch = 50
	}
	if cfg.SynthRequest.Volume == 0 {
		cfg.SynthRequest.Volume = 100
	}
	return &Engine{
		access: access, store: store, presenter: presenter, braille: brailleTranslator,
		synth: synthDriver, audioSink: sink, logger: logger, cfg: cfg, singleLetter: true,
		cursor: model.Cursor{Mode: "browse"}, review: model.Cursor{Mode: "object"},
		speechMode: "talk", brailleTether: "auto",
	}
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
	e.mu.RLock()
	preferred := model.ObjectID{}
	focusAtStart := e.focus
	if e.webContentFocused {
		preferred = focusAtStart
		if document, ok := e.graph.DocumentRoot(focusAtStart); ok {
			preferred = document
		}
	}
	e.mu.RUnlock()
	graph, err := e.access.BrowserGraph(ctx, e.cfg.BrowserProcess, preferred)
	if err != nil {
		return err
	}
	e.mu.Lock()
	oldGraph := e.graph
	e.graph = graph
	e.liveRegionKnown = make(map[model.ObjectID]bool, len(graph.Nodes))
	for id := range graph.Nodes {
		e.liveRegionKnown[id] = true
	}
	e.graphDirty = e.webContentFocused && e.focus != focusAtStart
	if _, ok := graph.Node(e.cursor.Object); !ok {
		e.cursor.Object = remapCursor(oldGraph, graph, e.cursor.Object)
		e.cursor.Offset = 0
	}
	if _, ok := graph.Node(e.navigator); !ok {
		e.navigator = remapCursor(oldGraph, graph, e.navigator)
		e.review.Object, e.review.Offset = e.navigator, 0
	}
	if _, ok := graph.Node(e.review.Object); !ok {
		e.review.Object, e.review.Offset = e.navigator, 0
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
	baseline := e.presenter.Settings()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.activeSession != "" {
		return errors.New("another test session is active")
	}
	e.activeSession = id
	e.presentationBaseline = baseline
	e.presentationBaselineSet = true
	e.recentLiveRegions = make(map[liveRegionAnnouncement]time.Time)
	return nil
}

func (e *Engine) EndSession(id string) {
	var baseline profile.PresentationSettings
	restore := false
	e.mu.Lock()
	if e.activeSession == id {
		baseline = e.presentationBaseline.Clone()
		restore = e.presentationBaselineSet
		e.activeSession = ""
		e.activeAction = ""
		e.presentationBaseline = profile.PresentationSettings{}
		e.presentationBaselineSet = false
		if restore {
			e.brailleTether = string(baseline.BrailleTether)
		}
	}
	e.mu.Unlock()
	if restore {
		_ = e.presenter.SetSettings(baseline)
	}
}

func (e *Engine) BeginAction(sessionID, commandID string) error {
	if commandID == "" {
		return errors.New("command id is empty")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if sessionID == "" || e.activeSession != sessionID {
		return errors.New("session is not active")
	}
	if e.activeAction != "" {
		return errors.New("another action is active")
	}
	e.activeAction = commandID
	return nil
}

func (e *Engine) EndAction(sessionID, commandID string) {
	e.mu.Lock()
	if e.activeSession == sessionID && e.activeAction == commandID {
		e.activeAction = ""
	}
	e.mu.Unlock()
}

func (e *Engine) causalEventCommand() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.activeAction != "" {
		return e.activeAction
	}
	return "event"
}

func (e *Engine) causalFocusCommand() string {
	command := e.causalEventCommand()
	switch command {
	case "nextFocusable", "previousFocusable", "returnToPage", "exitEmbeddedObject",
		"moveFocusToReviewPosition", "activate", "activateWithSpace", "activateNavigatorObject",
		"leftMouseClick", "rightMouseClick", "brailleRoute":
		return command
	default:
		return ""
	}
}

func (e *Engine) PresentationSettings() profile.PresentationSettings {
	return e.presenter.Settings()
}

func (e *Engine) SetPresentationSettings(sessionID string, settings profile.PresentationSettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	e.mu.RLock()
	active := e.activeSession == sessionID && sessionID != ""
	e.mu.RUnlock()
	if !active {
		return errors.New("session is not active")
	}
	if err := e.presenter.SetSettings(settings); err != nil {
		return err
	}
	e.mu.Lock()
	if e.activeSession != sessionID {
		e.mu.Unlock()
		return errors.New("session is not active")
	}
	e.brailleTether = string(settings.BrailleTether)
	e.mu.Unlock()
	return nil
}

func (e *Engine) ResetPresentationSettings(sessionID string) (profile.PresentationSettings, error) {
	e.mu.RLock()
	if e.activeSession != sessionID || sessionID == "" || !e.presentationBaselineSet {
		e.mu.RUnlock()
		return profile.PresentationSettings{}, errors.New("session is not active")
	}
	baseline := e.presentationBaseline.Clone()
	e.mu.RUnlock()
	if err := e.SetPresentationSettings(sessionID, baseline); err != nil {
		return profile.PresentationSettings{}, err
	}
	return baseline, nil
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
	cursorInDocument := false
	if e.graph != nil {
		revision = e.graph.Revision
		_, cursorInDocument = e.graph.DocumentRoot(e.cursor.Object)
	}
	state := State{Ready: e.ready, GraphRevision: revision, Cursor: e.cursor, Navigator: e.navigator, Review: e.review, CursorInDocument: cursorInDocument, Focus: e.focus, BrowserWindowActive: e.browserWindowActive, WebContentFocused: e.webContentFocused, SingleLetterNavigation: e.singleLetter, NativeSelectionMode: e.nativeSelection, LastSequence: e.store.Cursor()}
	state.MouseX, state.MouseY, state.MousePositionKnown = e.mouseX, e.mouseY, e.mousePositionKnown
	state.LeftMouseLocked, state.RightMouseLocked = e.leftMouseLocked, e.rightMouseLocked
	state.SpeechMode, state.SpeechPaused = e.speechMode, e.speechPaused
	state.BrailleOffset, state.BrailleTether = e.brailleOffset, e.brailleTether
	if e.reviewCopySet {
		value := e.reviewCopyStart
		state.ReviewCopyStart = &value
	}
	if e.reviewSelectionSet {
		value := e.reviewSelection
		state.ReviewSelection = &value
	}
	return state
}

func (e *Engine) DocumentSnapshot() []model.Node {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.graph == nil {
		return nil
	}
	return e.graph.Snapshot()
}

// Sync rebuilds the accessibility graph only when an AT-SPI structural event
// invalidated it. Artifact and document exports use this so they never freeze a
// pre-mutation graph merely because no navigation command followed the change.
func (e *Engine) Sync(ctx context.Context) error {
	return e.refreshIfDirty(ctx)
}

func (e *Engine) HandleGesture(ctx context.Context, gesture string) (bool, error) {
	command, ok := profile.CommandByGesture(gesture, e.cfg.KeyboardLayout)
	if !ok {
		return false, nil
	}
	consume, err := e.execute(ctx, command, "", true)
	return consume, err
}

// HandlePhysicalGesture executes a command after the control server prepared
// its graph. It must not query AT-SPI from a synchronous device callback.
func (e *Engine) HandlePhysicalGesture(ctx context.Context, gesture string) (bool, error) {
	command, ok := profile.CommandByGesture(gesture, e.cfg.KeyboardLayout)
	if !ok {
		return false, nil
	}
	return e.execute(ctx, command, "", false)
}

func (e *Engine) PreparePhysicalCommand(ctx context.Context, commandID string) error {
	command, ok := profile.CommandByID(commandID)
	if !ok {
		return fmt.Errorf("unsupported command %q", commandID)
	}
	if !profile.CommandNeedsGraph(command) {
		return nil
	}
	return e.refreshIfDirty(ctx)
}

// ShouldConsumeGesture answers synchronously without touching AT-SPI. Device
// callbacks use it before queuing command execution outside the D-Bus callback,
// avoiding a reentrant accessibility-bus deadlock during graph refresh.
func (e *Engine) ShouldConsumeGesture(gesture string) bool {
	command, ok := profile.CommandByGesture(gesture, e.cfg.KeyboardLayout)
	if !ok {
		return false
	}
	e.mu.RLock()
	mode, singleLetter := e.cursor.Mode, e.singleLetter
	e.mu.RUnlock()
	return commandConsumes(command, mode, singleLetter)
}

func commandConsumes(command profile.Command, mode string, singleLetter bool) bool {
	if !command.ConsumesBrowse {
		return false
	}
	switch command.Category {
	case "focus":
		return false
	case "quickNavigation":
		return mode == "browse" && singleLetter
	case "text", "activation", "table", "browseDocument":
		return mode == "browse"
	default:
		// NVDA-modifier commands remain global in focus mode. They must be
		// preempted before Chromium can handle keys such as F10 or Space.
		return true
	}
}

func (e *Engine) ExecuteDirect(ctx context.Context, commandID string) error {
	command, ok := profile.CommandByID(commandID)
	if !ok {
		return fmt.Errorf("unsupported command %q", commandID)
	}
	_, err := e.execute(ctx, command, "", true)
	return err
}

func (e *Engine) ExecuteDirectWithArgument(ctx context.Context, commandID, argument string) error {
	command, ok := profile.CommandByID(commandID)
	if !ok {
		return fmt.Errorf("unsupported command %q", commandID)
	}
	_, err := e.execute(ctx, command, argument, true)
	return err
}

func (e *Engine) execute(ctx context.Context, command profile.Command, argument string, refreshGraph bool) (bool, error) {
	e.mu.RLock()
	sessionID := e.activeSession
	mode := e.cursor.Mode
	singleLetter := e.singleLetter
	e.mu.RUnlock()
	e.store.Append(events.Event{Kind: events.KindCommandStarted, SessionID: sessionID, CausalCommand: command.ID, Text: command.Label, Provenance: events.ProvenanceAdapterLifecycle})
	consume := commandConsumes(command, mode, singleLetter)
	var err error
	if refreshGraph && profile.CommandNeedsGraph(command) {
		err = e.refreshIfDirty(ctx)
	}
	if err != nil {
		e.store.Append(events.Event{Kind: events.KindCommandSettled, SessionID: sessionID, CausalCommand: command.ID, Reason: err.Error(), Provenance: events.ProvenanceAdapterLifecycle})
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
	case "tableReport":
		err = e.reportTableAxis(command)
	case "dialog":
		err = e.dialog(command, argument)
	case "browseDocument":
		err = e.browseDocument(ctx, command)
	case "object":
		err = e.objectCommand(ctx, command)
	case "review":
		err = e.reviewCommand(ctx, command)
	case "mouse":
		err = e.mouseCommand(ctx, command)
	case "speechControl":
		err = e.speechControl(command)
	case "brailleControl":
		err = e.brailleControl(command, argument)
	case "focus":
		consume = false
	}
	reason := "completed"
	if err != nil {
		reason = err.Error()
	}
	e.store.Append(events.Event{Kind: events.KindCommandSettled, SessionID: sessionID, CausalCommand: command.ID, Reason: reason, Provenance: events.ProvenanceAdapterLifecycle})
	return consume, err
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
	matcher := profile.MatchTarget(command.Target)
	currentNode := graph.Nodes[current]
	switch command.Target {
	case "verticalParagraph":
		desiredX := 0
		if currentNode != nil {
			desiredX = currentNode.Bounds.X
		}
		matcher = func(node *model.Node) bool {
			return node != nil && node.SpokenContent() != "" && node.Bounds.X == desiredX &&
				(strings.EqualFold(node.Role, "paragraph") || strings.EqualFold(node.Attributes["tag"], "p"))
		}
	case "sameStyle", "differentStyle":
		desiredStyle := nodeStyleSignature(currentNode)
		matcher = func(node *model.Node) bool {
			candidateStyle := nodeStyleSignature(node)
			if node == nil || node.SpokenContent() == "" || candidateStyle == "" || desiredStyle == "" {
				return false
			}
			if command.Target == "sameStyle" {
				return candidateStyle == desiredStyle
			}
			return candidateStyle != desiredStyle
		}
	}
	var node *model.Node
	var ok bool
	if command.Target == "notLinkBlock" {
		node, ok = findNotLinkBlock(graph, current, command.Direction)
	} else {
		node, ok = graph.MoveInDocument(current, command.Direction, matcher)
	}
	if ok {
		e.cursor.Object, e.cursor.Offset = node.ID, 0
	}
	var table, tableCell, firstContainerItem *model.Node
	containerItemCount := 0
	if ok && command.Target == "table" {
		table = node
		grid, maxRow, maxColumn := tableGrid(graph, node.ID)
		if command.Direction > 0 {
			tableCell = firstPositionedCell(grid, maxRow, maxColumn)
		} else {
			tableCell = lastPositionedCell(grid, maxRow, maxColumn)
		}
		if tableCell != nil {
			e.cursor.Object = tableCell.ID
		}
	}
	if ok && (command.Target == "landmark" || command.Target == "list") {
		firstContainerItem = firstReadableDescendant(graph, node.ID)
		if command.Target == "list" {
			containerItemCount = len(node.Children)
		}
	}
	e.mu.Unlock()
	if !ok {
		e.emit(e.presenter.NoTarget(command.Target, command.Direction), nil, command.ID, "navigationBoundary", "normal")
		return nil
	}
	presentation := e.presenter.Present(node, "quickNavigation")
	switch command.Target {
	case "textParagraph":
		presentation = e.presenter.PresentTextParagraph(node)
	case "error":
		presentation = e.presenter.PresentTextError(node)
	case "table":
		presentation = e.presenter.PresentTableEntry(table, tableCell)
	case "landmark":
		presentation = e.presenter.PresentLandmarkEntry(node, firstContainerItem)
	case "list":
		presentation = e.presenter.PresentListEntry(node, firstContainerItem, containerItemCount)
	}
	e.emit(presentation, node, command.ID, "quickNavigation", "normal")
	return nil
}

func firstReadableDescendant(graph *model.Graph, parent model.ObjectID) *model.Node {
	if graph == nil {
		return nil
	}
	start := graph.Index(parent)
	for index := start + 1; start >= 0 && index < len(graph.Order); index++ {
		id := graph.Order[index]
		if !isDescendantOf(graph, id, parent) {
			break
		}
		node := graph.Nodes[id]
		if node != nil && strings.TrimSpace(node.SpokenContent()) != "" {
			return node
		}
	}
	return nil
}

const notLinkBlockMinimumLength = 30

// findNotLinkBlock follows NVDA's browseMode._iterNotLinkBlock semantics: find
// consecutive links with at least 30 characters of non-link content between
// them, then land at the start of that content range in either direction.
func findNotLinkBlock(graph *model.Graph, current model.ObjectID, direction int) (*model.Node, bool) {
	if graph == nil || direction == 0 {
		return nil, false
	}
	document, ok := graph.DocumentRoot(current)
	if !ok {
		return nil, false
	}
	currentIndex := graph.Index(current)
	links := make([]int, 0)
	if direction > 0 {
		for index := currentIndex + 1; index < len(graph.Order); index++ {
			node := graph.Nodes[graph.Order[index]]
			if node != nil && graph.InDocument(node.ID, document) && profile.MatchTarget("link")(node) {
				links = append(links, index)
			}
		}
	} else {
		for index := currentIndex - 1; index >= 0; index-- {
			node := graph.Nodes[graph.Order[index]]
			if node != nil && graph.InDocument(node.ID, document) && profile.MatchTarget("link")(node) {
				links = append(links, index)
			}
		}
	}
	for index := 0; index+1 < len(links); index++ {
		left, right := links[index], links[index+1]
		if left > right {
			left, right = right, left
		}
		firstLink := graph.Order[left]
		lastLink := graph.Order[right]
		length := 0
		var target *model.Node
		for position := left + 1; position < right; position++ {
			node := graph.Nodes[graph.Order[position]]
			if node == nil || isDescendantOf(graph, node.ID, firstLink) || isDescendantOf(graph, node.ID, lastLink) {
				continue
			}
			content := strings.TrimSpace(node.SpokenContent())
			if content == "" {
				continue
			}
			length += len([]rune(content)) + 1
			if target == nil && !redundantTextLeaf(graph, node) {
				target = node
			}
		}
		if length >= notLinkBlockMinimumLength && target != nil {
			return target, true
		}
	}
	return nil, false
}

func redundantTextLeaf(graph *model.Graph, node *model.Node) bool {
	if graph == nil || node == nil || !strings.EqualFold(node.Role, "static") {
		return false
	}
	parent := graph.Nodes[node.Parent]
	return parent != nil && strings.EqualFold(strings.TrimSpace(parent.SpokenContent()), strings.TrimSpace(node.SpokenContent()))
}

func nodeStyleSignature(node *model.Node) string {
	if node == nil {
		return ""
	}
	attributes := map[string]string{}
	if len(node.TextAttributeRuns) > 0 {
		for name, value := range node.TextAttributeRuns[0].Attributes {
			attributes[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
		}
	}
	for _, name := range []string{"font-family", "font-size", "font-weight", "font-style", "color", "background-color", "text-position", "underline", "strikethrough"} {
		if value := strings.TrimSpace(node.Attributes[name]); value != "" {
			attributes[name] = value
		}
	}
	if len(attributes) == 0 {
		return ""
	}
	keys := make([]string, 0, len(attributes))
	for key := range attributes {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+attributes[key])
	}
	return strings.Join(parts, "\x00")
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
			if first := firstDocumentBrowseNode(graph, document); first != nil {
				e.cursor.Object = first.ID
				node = first
			} else {
				node = graph.Nodes[document]
			}
			e.cursor.Offset = 0
		}
		e.mu.Unlock()
		e.emit(e.presenter.Present(node, "textNavigation"), node, command.ID, "textNavigation", "normal")
		return nil
	} else if command.ID == "documentEnd" {
		if document, ok := graph.DocumentRoot(e.cursor.Object); ok {
			for index := len(graph.Order) - 1; index >= 0; index-- {
				if graph.InDocument(graph.Order[index], document) {
					e.cursor.Object = graph.Order[index]
					node = graph.Nodes[e.cursor.Object]
					break
				}
			}
			e.cursor.Offset = len([]rune(navigableText(node)))
		}
		e.mu.Unlock()
		e.emit(e.presenter.Blank(), node, command.ID, "textNavigation", "normal")
		return nil
	} else if command.ID == "nextParagraphText" || command.ID == "previousParagraphText" {
		next, ok := graph.MoveInDocument(e.cursor.Object, command.Direction, func(candidate *model.Node) bool {
			return documentBlockNode(graph, candidate)
		})
		if ok {
			e.cursor.Object, e.cursor.Offset, node = next.ID, 0, next
		}
		e.mu.Unlock()
		if !ok {
			e.emit(e.presenter.NoTarget("text paragraph", command.Direction), nil, command.ID, "navigationBoundary", "normal")
			return nil
		}
		e.emit(e.presenter.Present(node, "textNavigation"), node, command.ID, "textNavigation", "normal")
		return nil
	} else if (command.ID == "nextLine" || command.ID == "previousLine") && !isTableCell(node) && !strings.Contains(navigableText(node), "\n") {
		line, ok := moveDocumentLine(graph, e.cursor.Object, command.Direction)
		if ok {
			e.cursor.Object, e.cursor.Offset, node = line[0].ID, 0, line[0]
		}
		e.mu.Unlock()
		if !ok {
			e.emit(e.presenter.NoTarget("line", command.Direction), nil, command.ID, "navigationBoundary", "normal")
			return nil
		}
		e.emit(e.presenter.PresentLine(line), node, command.ID, "textNavigation", "normal")
		return nil
	}
	unit, ok := textUnit(command.ID)
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("unsupported text command %q", command.ID)
	}
	text := navigableText(node)
	rangeValue, found, err := model.MoveTextUnit(node.ID, text, e.cursor.Offset, command.Direction, unit)
	if err != nil {
		e.mu.Unlock()
		return err
	}
	if !found {
		node, text, rangeValue, found = adjacentTextUnit(graph, e.cursor.Object, command.Direction, unit)
	}
	if found {
		e.cursor.Object, e.cursor.Offset = node.ID, rangeValue.Start
	}
	e.mu.Unlock()
	if !found {
		e.emit(e.presenter.NoTarget(string(unit), command.Direction), nil, command.ID, "navigationBoundary", "normal")
		return nil
	}
	fragment, err := rangeValue.Text(text)
	if err != nil {
		return err
	}
	e.emit(e.presenter.PresentText(node, fragment, unit), node, command.ID, "textNavigation", "normal")
	return nil
}

func firstDocumentBrowseNode(graph *model.Graph, document model.ObjectID) *model.Node {
	if graph == nil {
		return nil
	}
	start := graph.Index(document)
	for index := start + 1; start >= 0 && index < len(graph.Order); index++ {
		node := graph.Nodes[graph.Order[index]]
		if node == nil || !graph.InDocument(node.ID, document) {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(node.Role))
		if role == "landmark" || role == "document web" || role == "document frame" {
			continue
		}
		if strings.TrimSpace(node.SpokenContent()) != "" {
			return node
		}
	}
	return nil
}

const virtualLineWidth = 110

func moveDocumentLine(graph *model.Graph, current model.ObjectID, direction int) ([]*model.Node, bool) {
	document, ok := graph.DocumentRoot(current)
	if !ok {
		return nil, false
	}
	groups := documentLineGroups(graph, document)
	currentGroup := -1
	for index, group := range groups {
		for _, node := range group {
			if node.ID == current || isDescendantOf(graph, current, node.ID) || isDescendantOf(graph, node.ID, current) {
				currentGroup = index
				break
			}
		}
		if currentGroup >= 0 {
			break
		}
	}
	if currentGroup < 0 {
		return nil, false
	}
	next := currentGroup + direction
	if next < 0 || next >= len(groups) {
		return nil, false
	}
	return groups[next], true
}

func documentLineGroups(graph *model.Graph, document model.ObjectID) [][]*model.Node {
	groups := make([][]*model.Node, 0)
	inline := make([]*model.Node, 0)
	inlineLength := 0
	flush := func() {
		if len(inline) > 0 {
			groups = append(groups, inline)
			inline = nil
			inlineLength = 0
		}
	}
	for _, id := range graph.Order {
		node := graph.Nodes[id]
		if node == nil || !graph.InDocument(id, document) || !lineNode(graph, node) {
			continue
		}
		contentLength := len([]rune(strings.TrimSpace(node.SpokenContent())))
		if documentBlockNode(graph, node) {
			flush()
			groups = append(groups, []*model.Node{node})
			continue
		}
		separator := 0
		if len(inline) > 0 {
			separator = 1
		}
		if len(inline) > 0 && inlineLength+separator+contentLength > virtualLineWidth {
			flush()
			separator = 0
		}
		inline = append(inline, node)
		inlineLength += separator + contentLength
	}
	flush()
	return groups
}

func lineNode(graph *model.Graph, node *model.Node) bool {
	if node == nil || strings.TrimSpace(node.SpokenContent()) == "" || redundantTextLeaf(graph, node) {
		return false
	}
	role := strings.ToLower(strings.TrimSpace(node.Role))
	return role != "document web" && role != "document frame" && role != "landmark"
}

func documentBlockNode(graph *model.Graph, node *model.Node) bool {
	if !lineNode(graph, node) {
		return false
	}
	display := strings.ToLower(strings.TrimSpace(node.Attributes["display"]))
	if slices.Contains([]string{"block", "list-item", "table", "table-row", "table-cell"}, display) {
		return true
	}
	role := strings.ToLower(strings.TrimSpace(node.Role))
	return slices.Contains([]string{"heading", "paragraph", "article", "blockquote", "list item", "table cell", "row header", "column header"}, role)
}

func (e *Engine) browseDocument(ctx context.Context, command profile.Command) error {
	switch command.ID {
	case "refreshBrowseDocument":
		if err := e.Refresh(ctx); err != nil {
			return err
		}
		e.emit(e.presenter.Refreshed(), nil, command.ID, "documentRefresh", "normal")
		return nil
	case "toggleNativeSelection":
		e.mu.Lock()
		e.nativeSelection = !e.nativeSelection
		enabled := e.nativeSelection
		e.mu.Unlock()
		e.emit(e.presenter.NativeSelectionMode(enabled), nil, command.ID, "mode", "normal")
		return nil
	case "moveToContainerStart", "movePastContainerEnd":
		return e.moveContainer(command)
	case "exitEmbeddedObject":
		return e.exitEmbeddedObject(ctx, command.ID)
	default:
		return fmt.Errorf("unsupported browse-document command %q", command.ID)
	}
}

func (e *Engine) objectCommand(ctx context.Context, command profile.Command) error {
	e.mu.Lock()
	graph := e.graph
	if graph == nil {
		e.mu.Unlock()
		return errors.New("accessible graph is unavailable")
	}
	current := e.navigator
	if _, ok := graph.Node(current); !ok {
		current = e.focus
	}
	if _, ok := graph.Node(current); !ok {
		current = e.cursor.Object
	}
	if _, ok := graph.Node(current); !ok {
		current = initialCursor(graph)
	}
	currentNode := graph.Nodes[current]
	if currentNode == nil {
		e.mu.Unlock()
		e.emit(e.presenter.ObjectBoundary("navigator"), nil, command.ID, "objectNavigationBoundary", "normal")
		return nil
	}
	e.navigator = current
	if !e.review.Object.Valid() || graph.Nodes[e.review.Object] == nil {
		e.review.Object, e.review.Offset = current, 0
	}

	var target *model.Node
	boundary := ""
	switch command.ID {
	case "reportCurrentObject":
		target = currentNode
	case "moveToContainingObject":
		if parent := simpleObjectParent(graph, current); parent.Valid() {
			target = graph.Nodes[parent]
		} else {
			boundary = "containing"
		}
	case "moveToPreviousObject":
		target = simpleObjectSibling(graph, current, -1)
		if target == nil {
			boundary = "previous"
		}
	case "moveToNextObject":
		target = simpleObjectSibling(graph, current, 1)
		if target == nil {
			boundary = "next"
		}
	case "moveToPreviousObjectFlat", "moveToNextObjectFlat":
		target, _ = graph.Move(current, command.Direction, nil)
		if target == nil {
			if command.Direction < 0 {
				boundary = "previous"
			} else {
				boundary = "next"
			}
		}
	case "moveToFirstContainedObject":
		target = firstSimpleObjectChild(graph, current)
		if target == nil {
			boundary = "inside"
		}
	case "moveToFocusObject":
		target = graph.Nodes[e.focus]
		if target == nil {
			boundary = "focus"
		}
	case "activateNavigatorObject":
		id := e.review.Object
		if !id.Valid() || graph.Nodes[id] == nil {
			id = current
		}
		e.mu.Unlock()
		return e.activateObject(ctx, graph, id, command.ID)
	case "moveFocusToReviewPosition":
		id := e.review.Object
		if !id.Valid() || graph.Nodes[id] == nil {
			id = current
		}
		e.mu.Unlock()
		e.emit(e.presenter.MoveFocus(), graph.Nodes[id], command.ID, "objectFocus", "normal")
		return e.access.GrabFocus(ctx, id)
	case "reportReviewLocation":
		reviewNode := graph.Nodes[e.review.Object]
		if reviewNode == nil {
			reviewNode = currentNode
		}
		e.mu.Unlock()
		e.emit(e.presenter.CaretLocation(reviewNode.Bounds), reviewNode, command.ID, "reviewLocation", "normal")
		return nil
	default:
		e.mu.Unlock()
		return fmt.Errorf("unsupported object command %q", command.ID)
	}

	if target != nil && command.ID != "reportCurrentObject" {
		e.navigator = target.ID
		e.review.Object, e.review.Offset = target.ID, 0
	}
	e.mu.Unlock()
	if boundary != "" {
		e.emit(e.presenter.ObjectBoundary(boundary), nil, command.ID, "objectNavigationBoundary", "normal")
		return nil
	}
	e.emit(e.presenter.Present(target, "objectNavigation"), target, command.ID, "objectNavigation", "normal")
	return nil
}

func simpleObjectParent(graph *model.Graph, id model.ObjectID) model.ObjectID {
	if graph == nil {
		return model.ObjectID{}
	}
	node := graph.Nodes[id]
	if node == nil {
		return model.ObjectID{}
	}
	for parent, steps := node.Parent, 0; parent.Valid() && steps < 512; steps++ {
		candidate := graph.Nodes[parent]
		if candidate == nil {
			return model.ObjectID{}
		}
		if candidate.Semantic() {
			return parent
		}
		parent = candidate.Parent
	}
	return model.ObjectID{}
}

func firstSimpleObjectChild(graph *model.Graph, parent model.ObjectID) *model.Node {
	if graph == nil {
		return nil
	}
	for _, id := range graph.Order {
		if simpleObjectParent(graph, id) == parent {
			return graph.Nodes[id]
		}
	}
	return nil
}

func simpleObjectSibling(graph *model.Graph, current model.ObjectID, direction int) *model.Node {
	parent := simpleObjectParent(graph, current)
	if !parent.Valid() || direction == 0 {
		return nil
	}
	siblings := make([]model.ObjectID, 0)
	for _, id := range graph.Order {
		if simpleObjectParent(graph, id) == parent {
			siblings = append(siblings, id)
		}
	}
	index := slices.Index(siblings, current)
	if index < 0 || index+direction < 0 || index+direction >= len(siblings) {
		return nil
	}
	return graph.Nodes[siblings[index+direction]]
}

func (e *Engine) activateObject(ctx context.Context, graph *model.Graph, id model.ObjectID, commandID string) error {
	for steps := 0; id.Valid() && steps < 512; steps++ {
		if err := e.access.DoDefaultAction(ctx, id); err == nil {
			e.emit(e.presenter.Activate(), graph.Nodes[id], commandID, "objectActivation", "normal")
			return nil
		}
		node := graph.Nodes[id]
		if node == nil {
			break
		}
		id = node.Parent
	}
	e.emit(e.presenter.NoAction(), nil, commandID, "objectActivationBoundary", "normal")
	return nil
}

func (e *Engine) reviewCommand(ctx context.Context, command profile.Command) error {
	e.mu.Lock()
	graph := e.graph
	node := graphNode(graph, e.review.Object)
	if node == nil {
		node = graphNode(graph, e.navigator)
		if node != nil {
			e.review.Object, e.review.Offset = node.ID, 0
		}
	}
	if node == nil {
		e.mu.Unlock()
		e.emit(e.presenter.ObjectBoundary("navigator"), nil, command.ID, "reviewBoundary", "normal")
		return nil
	}
	text := navigableText(node)
	if text == "" {
		text = node.SpokenContent()
	}
	runeCount := len([]rune(text))
	if runeCount == 0 {
		e.mu.Unlock()
		e.emit(e.presenter.ReviewBoundary("empty"), node, command.ID, "reviewBoundary", "normal")
		return nil
	}
	if e.review.Offset < 0 {
		e.review.Offset = 0
	}
	if e.review.Offset >= runeCount {
		e.review.Offset = runeCount - 1
	}

	var value model.TextRange
	var presentation profile.Presentation
	var err error
	switch command.ID {
	case "reviewTopLine":
		value, err = firstTextRange(node.ID, text, model.TextUnitLine)
	case "reviewBottomLine":
		value, err = lastTextRange(node.ID, text, model.TextUnitLine)
	case "reviewCurrentLine":
		value, err = textRangeAt(node.ID, text, e.review.Offset, model.TextUnitLine)
	case "reviewPreviousLine", "reviewNextLine":
		value, presentation, err = moveReviewRange(node.ID, text, e.review.Offset, command.Direction, model.TextUnitLine, e.presenter)
	case "reviewCurrentWord":
		value, err = textRangeAt(node.ID, text, e.review.Offset, model.TextUnitWord)
	case "reviewPreviousWord", "reviewNextWord":
		value, presentation, err = moveReviewRange(node.ID, text, e.review.Offset, command.Direction, model.TextUnitWord, e.presenter)
	case "reviewCurrentCharacter":
		value, err = model.CharacterRange(node.ID, text, e.review.Offset)
	case "reviewPreviousCharacter", "reviewNextCharacter":
		value, presentation, err = moveReviewCharacter(node.ID, text, e.review.Offset, command.Direction, e.presenter)
	case "reviewLineStart", "reviewLineEnd":
		line, rangeErr := textRangeAt(node.ID, text, e.review.Offset, model.TextUnitLine)
		if rangeErr != nil {
			err = rangeErr
			break
		}
		offset := line.Start
		if command.ID == "reviewLineEnd" {
			offset = max(line.Start, line.End-1)
		}
		value, err = model.CharacterRange(node.ID, text, offset)
	case "reviewPreviousPage", "reviewNextPage":
		e.mu.Unlock()
		e.emit(e.presenter.ReviewBoundary("pageUnsupported"), node, command.ID, "reviewBoundary", "normal")
		return nil
	case "reviewSelectionStart", "reviewSelectionEnd":
		if len(node.Selections) == 0 || node.Selections[0].End <= node.Selections[0].Start {
			e.mu.Unlock()
			e.emit(e.presenter.ReviewBoundary("noSelection"), node, command.ID, "reviewBoundary", "normal")
			return nil
		}
		offset := node.Selections[0].Start
		if command.ID == "reviewSelectionEnd" {
			offset = node.Selections[0].End - 1
		}
		value, err = model.CharacterRange(node.ID, text, offset)
	case "sayAllReview":
		start := e.review.Offset
		value = model.TextRange{Object: node.ID, Start: start, End: runeCount}
		e.review.Offset = runeCount - 1
	case "setReviewCopyStart":
		e.reviewCopyStart = e.review
		e.reviewCopySet = true
		e.reviewSelectionSet = false
		e.mu.Unlock()
		e.emit(e.presenter.ReviewCopyStart(), node, command.ID, "reviewCopy", "normal")
		return nil
	case "moveToReviewCopyStart":
		if !e.reviewCopySet || e.reviewCopyStart.Object != node.ID {
			e.mu.Unlock()
			e.emit(e.presenter.ReviewBoundary("noCopyStart"), node, command.ID, "reviewBoundary", "normal")
			return nil
		}
		e.review.Offset = min(max(e.reviewCopyStart.Offset, 0), runeCount-1)
		value, err = model.CharacterRange(node.ID, text, e.review.Offset)
	case "copyToReviewPosition":
		if !e.reviewCopySet || e.reviewCopyStart.Object != node.ID {
			e.mu.Unlock()
			e.emit(e.presenter.ReviewBoundary("noCopyStart"), node, command.ID, "reviewBoundary", "normal")
			return nil
		}
		start, end := e.reviewCopyStart.Offset, e.review.Offset
		if start > end {
			start, end = end, start
		}
		end = min(end+1, runeCount)
		selection := model.TextRange{Object: node.ID, Start: start, End: end}
		e.reviewSelection, e.reviewSelectionSet = selection, true
		e.mu.Unlock()
		if err := e.access.SetTextSelection(ctx, node.ID, start, end); err != nil {
			return fmt.Errorf("set review selection: %w", err)
		}
		e.emit(e.presenter.ReviewSelected(end-start), node, command.ID, "reviewSelection", "normal")
		return nil
	case "reportReviewFormatting":
		offset := e.review.Offset
		e.mu.Unlock()
		e.emit(e.presenter.TextFormatting(node, offset), node, command.ID, "reviewFormatting", "normal")
		return nil
	case "nextReviewMode", "previousReviewMode":
		modes := []string{"object", "document", "screen"}
		index := slices.Index(modes, e.review.Mode)
		if index < 0 {
			index = 0
		}
		next := index + command.Direction
		if next < 0 || next >= len(modes) {
			kind := "noNextMode"
			if command.Direction < 0 {
				kind = "noPreviousMode"
			}
			e.mu.Unlock()
			e.emit(e.presenter.ReviewBoundary(kind), node, command.ID, "reviewBoundary", "normal")
			return nil
		}
		e.review.Mode = modes[next]
		mode := e.review.Mode
		e.mu.Unlock()
		e.emit(e.presenter.ReviewMode(mode), node, command.ID, "reviewMode", "normal")
		return nil
	default:
		e.mu.Unlock()
		return fmt.Errorf("unsupported review command %q", command.ID)
	}
	if err != nil {
		e.mu.Unlock()
		return err
	}
	if value.End > value.Start {
		e.review.Object, e.review.Offset = node.ID, value.Start
	}
	e.mu.Unlock()
	fragment, err := value.Text(text)
	if err != nil {
		return err
	}
	unit := model.TextUnitCharacter
	if strings.Contains(command.ID, "Line") {
		unit = model.TextUnitLine
	} else if strings.Contains(command.ID, "Word") {
		unit = model.TextUnitWord
	} else if command.ID == "sayAllReview" {
		unit = model.TextUnitLine
	}
	spoken := e.presenter.PresentText(node, fragment, unit)
	if presentation.Speech != "" || presentation.Braille != "" {
		spoken = joinPresentations(presentation, spoken)
	}
	e.emit(spoken, node, command.ID, "reviewText", "normal")
	return nil
}

func graphNode(graph *model.Graph, id model.ObjectID) *model.Node {
	if graph == nil {
		return nil
	}
	return graph.Nodes[id]
}

func firstTextRange(id model.ObjectID, text string, unit model.TextUnit) (model.TextRange, error) {
	ranges, err := model.TextUnitRanges(id, text, unit)
	if err != nil {
		return model.TextRange{}, err
	}
	if len(ranges) == 0 {
		return model.TextRange{}, errors.New("text has no review ranges")
	}
	return ranges[0], nil
}

func lastTextRange(id model.ObjectID, text string, unit model.TextUnit) (model.TextRange, error) {
	ranges, err := model.TextUnitRanges(id, text, unit)
	if err != nil {
		return model.TextRange{}, err
	}
	if len(ranges) == 0 {
		return model.TextRange{}, errors.New("text has no review ranges")
	}
	return ranges[len(ranges)-1], nil
}

func textRangeAt(id model.ObjectID, text string, offset int, unit model.TextUnit) (model.TextRange, error) {
	ranges, err := model.TextUnitRanges(id, text, unit)
	if err != nil {
		return model.TextRange{}, err
	}
	for _, value := range ranges {
		if offset >= value.Start && (offset < value.End || value.Start == value.End) {
			return value, nil
		}
	}
	if len(ranges) > 0 && offset >= ranges[len(ranges)-1].End {
		return ranges[len(ranges)-1], nil
	}
	return model.TextRange{}, errors.New("review position is outside text ranges")
}

func moveReviewRange(id model.ObjectID, text string, offset, direction int, unit model.TextUnit, presenter *profile.Presenter) (model.TextRange, profile.Presentation, error) {
	value, found, err := model.MoveTextUnit(id, text, offset, direction, unit)
	if err != nil {
		return model.TextRange{}, profile.Presentation{}, err
	}
	if found {
		return value, profile.Presentation{}, nil
	}
	value, err = textRangeAt(id, text, offset, unit)
	kind := "bottom"
	if direction < 0 {
		kind = "top"
	}
	return value, presenter.ReviewBoundary(kind), err
}

func moveReviewCharacter(id model.ObjectID, text string, offset, direction int, presenter *profile.Presenter) (model.TextRange, profile.Presentation, error) {
	line, err := textRangeAt(id, text, offset, model.TextUnitLine)
	if err != nil {
		return model.TextRange{}, profile.Presentation{}, err
	}
	target := offset + direction
	if target < line.Start || target >= line.End {
		kind := "right"
		if direction < 0 {
			kind = "left"
		}
		value, rangeErr := model.CharacterRange(id, text, offset)
		return value, presenter.ReviewBoundary(kind), rangeErr
	}
	value, err := model.CharacterRange(id, text, target)
	return value, profile.Presentation{}, err
}

func joinPresentations(first, second profile.Presentation) profile.Presentation {
	return profile.Presentation{
		Speech:         strings.TrimSpace(strings.Join(nonEmptyStrings(first.Speech, second.Speech), "  ")),
		Braille:        strings.TrimSpace(strings.Join(nonEmptyStrings(first.Braille, second.Braille), " ")),
		SpeechCommands: append(append([]events.SpeechCommand(nil), first.SpeechCommands...), second.SpeechCommands...),
	}
}

func nonEmptyStrings(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	return result
}

func (e *Engine) mouseCommand(ctx context.Context, command profile.Command) error {
	e.mu.Lock()
	graph := e.graph
	navigator := graphNode(graph, e.navigator)
	if navigator == nil {
		navigator = graphNode(graph, e.focus)
	}
	if navigator == nil {
		navigator = graphNode(graph, e.cursor.Object)
	}
	if !e.mousePositionKnown && navigator != nil && navigator.Bounds.Width > 0 && navigator.Bounds.Height > 0 {
		e.mouseX = navigator.Bounds.X + navigator.Bounds.Width/2
		e.mouseY = navigator.Bounds.Y + navigator.Bounds.Height/2
		e.mousePositionKnown = true
	}
	x, y := e.mouseX, e.mouseY

	switch command.ID {
	case "moveMouseToNavigatorObject":
		if navigator == nil || navigator.Bounds.Width <= 0 || navigator.Bounds.Height <= 0 {
			e.mu.Unlock()
			e.emit(e.presenter.MouseBoundary("noLocation"), navigator, command.ID, "mouseBoundary", "normal")
			return nil
		}
		x = navigator.Bounds.X + navigator.Bounds.Width/2
		y = navigator.Bounds.Y + navigator.Bounds.Height/2
		e.mouseX, e.mouseY, e.mousePositionKnown = x, y, true
		e.mu.Unlock()
		if err := e.access.GenerateMouseEvent(ctx, x, y, "abs"); err != nil {
			return fmt.Errorf("move mouse to navigator object: %w", err)
		}
		// NVDA moves the pointer silently. Command lifecycle events still prove
		// delivery; presenting the object here creates output NVDA never emits.
		return nil
	case "moveNavigatorToMouseObject":
		if !e.mousePositionKnown {
			e.mu.Unlock()
			e.emit(e.presenter.MouseBoundary("unknownPosition"), nil, command.ID, "mouseBoundary", "normal")
			return nil
		}
		target := objectAtPoint(graph, x, y)
		if target == nil {
			e.mu.Unlock()
			e.emit(e.presenter.MouseBoundary("noObject"), nil, command.ID, "mouseBoundary", "normal")
			return nil
		}
		e.navigator = target.ID
		e.review = model.Cursor{Object: target.ID, Offset: 0, Mode: "object"}
		e.mu.Unlock()
		e.emit(joinPresentations(e.presenter.MoveNavigatorToMouse(), e.presenter.Present(target, "mouse")), target, command.ID, "mouseNavigation", "normal")
		return nil
	case "leftMouseClick", "rightMouseClick":
		name := "b1c"
		button := "leftClick"
		if command.ID == "rightMouseClick" {
			name, button = "b3c", "rightClick"
		}
		known := e.mousePositionKnown
		e.mu.Unlock()
		if !known {
			e.emit(e.presenter.MouseBoundary("unknownPosition"), nil, command.ID, "mouseBoundary", "normal")
			return nil
		}
		if err := e.access.GenerateMouseEvent(ctx, x, y, name); err != nil {
			return fmt.Errorf("click mouse: %w", err)
		}
		e.emit(e.presenter.MouseState(button, true), navigator, command.ID, "mouseClick", "normal")
		return nil
	case "leftMouseLock", "rightMouseLock":
		left := command.ID == "leftMouseLock"
		locked := e.rightMouseLocked
		if left {
			locked = e.leftMouseLocked
		}
		locked = !locked
		if left {
			e.leftMouseLocked = locked
		} else {
			e.rightMouseLocked = locked
		}
		known := e.mousePositionKnown
		e.mu.Unlock()
		if !known {
			e.emit(e.presenter.MouseBoundary("unknownPosition"), nil, command.ID, "mouseBoundary", "normal")
			return nil
		}
		button, suffix := "b3", "r"
		kind := "rightLock"
		if left {
			button, kind = "b1", "leftLock"
		}
		if locked {
			suffix = "p"
		}
		if err := e.access.GenerateMouseEvent(ctx, x, y, button+suffix); err != nil {
			return fmt.Errorf("toggle mouse lock: %w", err)
		}
		e.emit(e.presenter.MouseState(kind, locked), navigator, command.ID, "mouseLock", "normal")
		return nil
	default:
		e.mu.Unlock()
		return fmt.Errorf("unsupported mouse command %q", command.ID)
	}
}

func objectAtPoint(graph *model.Graph, x, y int) *model.Node {
	if graph == nil {
		return nil
	}
	for index := len(graph.Order) - 1; index >= 0; index-- {
		node := graph.Nodes[graph.Order[index]]
		if node == nil || node.Bounds.Width <= 0 || node.Bounds.Height <= 0 {
			continue
		}
		if x >= node.Bounds.X && x < node.Bounds.X+node.Bounds.Width && y >= node.Bounds.Y && y < node.Bounds.Y+node.Bounds.Height {
			return node
		}
	}
	return nil
}

func (e *Engine) speechControl(command profile.Command) error {
	e.mu.Lock()
	switch command.ID {
	case "stopSpeech":
		if e.synthCancel != nil {
			e.synthCancel()
			e.synthCancel = nil
			e.synthDone = nil
		}
		e.speechPaused = false
		e.mu.Unlock()
		return nil
	case "pauseSpeech":
		e.speechPaused = !e.speechPaused
		e.mu.Unlock()
		return nil
	case "cycleSpeechMode":
		modes := []string{"off", "beeps", "talk", "on-demand"}
		index := slices.Index(modes, e.speechMode)
		if index < 0 {
			index = slices.Index(modes, "talk")
		}
		e.speechMode = modes[(index+1)%len(modes)]
		mode := e.speechMode
		e.mu.Unlock()
		e.emit(e.presenter.SpeechMode(mode), nil, command.ID, "speechMode", "normal")
		return nil
	default:
		e.mu.Unlock()
		return fmt.Errorf("unsupported speech-control command %q", command.ID)
	}
}

func (e *Engine) brailleControl(command profile.Command, argument string) error {
	e.mu.Lock()
	graph := e.graph
	node := graphNode(graph, e.review.Object)
	if e.brailleTether == "focus" {
		node = graphNode(graph, e.focus)
	} else if e.brailleTether == "auto" && e.focus.Valid() {
		node = graphNode(graph, e.focus)
	}
	if command.ID == "brailleToggleTether" {
		values := []string{"auto", "focus", "review"}
		index := slices.Index(values, e.brailleTether)
		if index < 0 {
			index = 0
		}
		e.brailleTether = values[(index+1)%len(values)]
		tether := e.brailleTether
		e.brailleOffset = 0
		e.mu.Unlock()
		e.emit(e.presenter.BrailleTether(tether), node, command.ID, "brailleTether", "normal")
		return nil
	}
	if node == nil {
		node = graphNode(graph, e.navigator)
	}
	if node == nil {
		e.mu.Unlock()
		return errors.New("braille source object is unavailable")
	}
	switch command.ID {
	case "braillePanBack", "braillePanForward":
		text := navigableText(node)
		if strings.Contains(text, "\n") {
			value, _, err := moveReviewRange(node.ID, text, e.review.Offset, command.Direction, model.TextUnitLine, e.presenter)
			if err != nil {
				e.mu.Unlock()
				return err
			}
			e.review.Object, e.review.Offset = node.ID, value.Start
			e.brailleOffset = value.Start
			e.mu.Unlock()
			fragment, err := value.Text(text)
			if err != nil {
				return err
			}
			e.emit(profile.Presentation{Braille: fragment}, node, command.ID, "braillePan", "normal")
			return nil
		}
		full := []rune(e.presenter.Present(node, "braille").Braille)
		if command.Direction < 0 {
			e.brailleOffset = max(0, e.brailleOffset-40)
		} else if len(full) > 0 {
			e.brailleOffset = min(max(0, len(full)-1), e.brailleOffset+40)
		}
		start := min(e.brailleOffset, len(full))
		end := min(start+40, len(full))
		fragment := string(full[start:end])
		e.mu.Unlock()
		e.emit(profile.Presentation{Braille: fragment}, node, command.ID, "braillePan", "normal")
		return nil
	case "braillePreviousLine", "brailleNextLine":
		text := navigableText(node)
		value, _, err := moveReviewRange(node.ID, text, e.review.Offset, command.Direction, model.TextUnitLine, e.presenter)
		if err != nil {
			e.mu.Unlock()
			return err
		}
		e.review.Object, e.review.Offset = node.ID, value.Start
		e.brailleOffset = 0
		e.mu.Unlock()
		fragment, err := value.Text(text)
		if err != nil {
			return err
		}
		e.emit(profile.Presentation{Braille: fragment}, node, command.ID, "brailleLine", "normal")
		return nil
	case "brailleRoute", "brailleReportFormatting":
		cell := 0
		if strings.TrimSpace(argument) != "" {
			parsed, err := strconv.Atoi(argument)
			if err != nil || parsed < 0 || parsed > 199 {
				e.mu.Unlock()
				return errors.New("braille cell must be an integer from 0 to 199")
			}
			cell = parsed
		}
		text := navigableText(node)
		offset := min(max(0, e.brailleOffset+cell), max(0, len([]rune(text))-1))
		if command.ID == "brailleReportFormatting" {
			e.mu.Unlock()
			e.emit(e.presenter.TextFormatting(node, offset), node, command.ID, "brailleFormatting", "normal")
			return nil
		}
		e.review.Object, e.review.Offset = node.ID, offset
		e.cursor.Object, e.cursor.Offset = node.ID, offset
		e.mu.Unlock()
		value, err := textRangeAt(node.ID, text, offset, model.TextUnitLine)
		if err != nil {
			return err
		}
		fragment, err := value.Text(text)
		if err != nil {
			return err
		}
		e.emit(profile.Presentation{Braille: fragment}, node, command.ID, "brailleRoute", "normal")
		return nil
	default:
		e.mu.Unlock()
		return fmt.Errorf("unsupported braille-control command %q", command.ID)
	}
}

func (e *Engine) moveContainer(command profile.Command) error {
	e.mu.Lock()
	graph, current := e.graph, e.cursor.Object
	if graph == nil {
		e.mu.Unlock()
		return errors.New("accessible graph is unavailable")
	}
	container := containingContainer(graph, current)
	if container == nil {
		e.mu.Unlock()
		e.emit(e.presenter.NotInContainer(), nil, command.ID, "navigationBoundary", "normal")
		return nil
	}
	if command.ID == "moveToContainerStart" {
		e.cursor.Object, e.cursor.Offset = container.ID, 0
		e.mu.Unlock()
		e.emit(e.presenter.Present(container, "quickNavigation"), container, command.ID, "containerNavigation", "normal")
		return nil
	}
	document, scoped := graph.DocumentRoot(current)
	containerIndex := graph.Index(container.ID)
	lastIndex := containerIndex
	for index := containerIndex + 1; index < len(graph.Order); index++ {
		if !isDescendantOf(graph, graph.Order[index], container.ID) {
			break
		}
		lastIndex = index
	}
	var next *model.Node
	for index := lastIndex + 1; index < len(graph.Order); index++ {
		candidate := graph.Nodes[graph.Order[index]]
		if candidate == nil || (scoped && !graph.InDocument(candidate.ID, document)) {
			continue
		}
		next = candidate
		break
	}
	if next != nil {
		e.cursor.Object, e.cursor.Offset = next.ID, 0
	}
	e.mu.Unlock()
	if next == nil {
		e.emit(e.presenter.Bottom(), nil, command.ID, "navigationBoundary", "normal")
		return nil
	}
	e.emit(e.presenter.Present(next, "textNavigation"), next, command.ID, "containerNavigation", "normal")
	return nil
}

func containingContainer(graph *model.Graph, id model.ObjectID) *model.Node {
	for steps := 0; id.Valid() && steps < 512; steps++ {
		node := graph.Nodes[id]
		if node == nil {
			return nil
		}
		if isContainer(node) {
			return node
		}
		id = node.Parent
	}
	return nil
}

func isContainer(node *model.Node) bool {
	if node == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(node.Role)) {
	case "list", "table", "article", "section", "grouping", "landmark", "blockquote", "menu", "tree", "page tab list":
		return true
	default:
		return false
	}
}

func isDescendantOf(graph *model.Graph, id, ancestor model.ObjectID) bool {
	for steps := 0; id.Valid() && steps < 512; steps++ {
		if id == ancestor {
			return true
		}
		node := graph.Nodes[id]
		if node == nil {
			return false
		}
		id = node.Parent
	}
	return false
}

func (e *Engine) exitEmbeddedObject(ctx context.Context, commandID string) error {
	e.mu.RLock()
	graph, current := e.graph, e.cursor.Object
	e.mu.RUnlock()
	if graph == nil {
		return errors.New("accessible graph is unavailable")
	}
	documents := make([]*model.Node, 0, 2)
	embeddedBoundary := false
	for id, steps := current, 0; id.Valid() && steps < 512; steps++ {
		node := graph.Nodes[id]
		if node == nil {
			break
		}
		role := strings.ToLower(strings.TrimSpace(node.Role))
		if len(documents) == 1 && role == "embedded" {
			embeddedBoundary = true
		}
		if role == "document web" || role == "document frame" {
			documents = append(documents, node)
		}
		id = node.Parent
	}
	if len(documents) < 2 || !embeddedBoundary {
		return nil
	}
	target := documents[1]
	if err := e.access.GrabFocus(ctx, target.ID); err != nil {
		return fmt.Errorf("focus containing document: %w", err)
	}
	e.mu.Lock()
	e.cursor.Object, e.cursor.Offset = target.ID, 0
	e.mu.Unlock()
	e.emit(e.presenter.Present(target, "focus"), target, commandID, "embeddedObjectExit", "normal")
	return nil
}

func textUnit(commandID string) (model.TextUnit, bool) {
	switch commandID {
	case "nextCharacter", "previousCharacter":
		return model.TextUnitCharacter, true
	case "nextWord", "previousWord":
		return model.TextUnitWord, true
	case "nextLine", "previousLine":
		return model.TextUnitLine, true
	default:
		return "", false
	}
}

func navigableText(node *model.Node) string {
	if node == nil {
		return ""
	}
	if node.IsProtected() {
		return ""
	}
	if node.Text != "" {
		// Chromium exposes embedded descendants in AT-SPI Text as U+FFFC.
		// HooVDA navigates those descendants as graph nodes, so the marker must
		// never become literal speech or braille.
		return strings.ReplaceAll(node.Text, "\ufffc", "")
	}
	if len(node.Children) == 0 {
		return node.SpokenContent()
	}
	return ""
}

func adjacentTextUnit(graph *model.Graph, current model.ObjectID, direction int, unit model.TextUnit) (*model.Node, string, model.TextRange, bool) {
	document, scoped := graph.DocumentRoot(current)
	index := graph.Index(current)
	if !scoped || index < 0 {
		return nil, "", model.TextRange{}, false
	}
	for next := index + direction; next >= 0 && next < len(graph.Order); next += direction {
		node := graph.Nodes[graph.Order[next]]
		if node == nil || !graph.InDocument(node.ID, document) {
			continue
		}
		text := navigableText(node)
		if text == "" {
			continue
		}
		ranges, err := model.TextUnitRanges(node.ID, text, unit)
		if err != nil || len(ranges) == 0 {
			continue
		}
		if direction > 0 {
			return node, text, ranges[0], true
		}
		return node, text, ranges[len(ranges)-1], true
	}
	return nil, "", model.TextRange{}, false
}

func (e *Engine) navigateTable(command profile.Command) error {
	e.mu.Lock()
	graph := e.graph
	if graph == nil {
		e.mu.Unlock()
		return errors.New("accessible graph is unavailable")
	}
	current := containingTableCell(graph, e.cursor.Object)
	tableID := containingTable(graph, e.cursor.Object)
	if !tableID.Valid() {
		e.mu.Unlock()
		return errors.New("cursor is not inside a table")
	}
	grid, maxRow, maxColumn := tableGrid(graph, tableID)
	if len(grid) == 0 {
		e.mu.Unlock()
		return errors.New("table has no positioned cells")
	}
	if current == nil {
		current = grid[[2]int{1, 1}]
		if current == nil {
			for row := 1; row <= maxRow && current == nil; row++ {
				for column := 1; column <= maxColumn; column++ {
					if grid[[2]int{row, column}] != nil {
						current = grid[[2]int{row, column}]
						break
					}
				}
			}
		}
	}
	if current == nil {
		e.mu.Unlock()
		return errors.New("table has no navigable cells")
	}
	targetRow, targetColumn := current.Row, current.Column
	rowStep, columnStep := 0, 0
	switch command.ID {
	case "previousTableColumn":
		targetColumn, columnStep = current.Column-1, -1
	case "nextTableColumn":
		targetColumn, columnStep = current.Column+max(1, current.ColumnSpan), 1
	case "previousTableRow":
		targetRow, rowStep = current.Row-1, -1
	case "nextTableRow":
		targetRow, rowStep = current.Row+max(1, current.RowSpan), 1
	case "firstTableColumn":
		targetColumn, columnStep = 1, 1
	case "lastTableColumn":
		targetColumn, columnStep = maxColumn, -1
	case "firstTableRow":
		targetRow, rowStep = 1, 1
	case "lastTableRow":
		targetRow, rowStep = maxRow, -1
	default:
		e.mu.Unlock()
		return fmt.Errorf("unsupported table command %q", command.ID)
	}
	next := grid[[2]int{targetRow, targetColumn}]
	for next == nil && targetRow >= 1 && targetRow <= maxRow && targetColumn >= 1 && targetColumn <= maxColumn {
		targetRow += rowStep
		targetColumn += columnStep
		next = grid[[2]int{targetRow, targetColumn}]
	}
	ok := next != nil && next.ID != current.ID
	if ok {
		e.cursor.Object, e.cursor.Offset = next.ID, 0
	}
	e.mu.Unlock()
	if !ok {
		e.emit(e.presenter.NoTarget("table cell", command.Direction), nil, command.ID, "tableBoundary", "normal")
		return nil
	}
	e.emit(e.presenter.PresentTableMove(next, current), next, command.ID, "tableNavigation", "normal")
	return nil
}

func firstPositionedCell(grid map[[2]int]*model.Node, maxRow, maxColumn int) *model.Node {
	for row := 1; row <= maxRow; row++ {
		for column := 1; column <= maxColumn; column++ {
			if cell := grid[[2]int{row, column}]; cell != nil {
				return cell
			}
		}
	}
	return nil
}

func lastPositionedCell(grid map[[2]int]*model.Node, maxRow, maxColumn int) *model.Node {
	for row := maxRow; row >= 1; row-- {
		for column := maxColumn; column >= 1; column-- {
			if cell := grid[[2]int{row, column}]; cell != nil {
				return cell
			}
		}
	}
	return nil
}

func isTableCell(node *model.Node) bool {
	if node == nil {
		return false
	}
	switch strings.ToLower(node.Role) {
	case "table cell", "cell", "row header", "column header":
		return true
	default:
		return false
	}
}

func containingTableCell(graph *model.Graph, id model.ObjectID) *model.Node {
	for steps := 0; id.Valid() && steps < 512; steps++ {
		node := graph.Nodes[id]
		if node == nil {
			return nil
		}
		if isTableCell(node) {
			return node
		}
		id = node.Parent
	}
	return nil
}

func containingTable(graph *model.Graph, id model.ObjectID) model.ObjectID {
	if cell := containingTableCell(graph, id); cell != nil && cell.Table.Valid() {
		return cell.Table
	}
	for steps := 0; id.Valid() && steps < 512; steps++ {
		node := graph.Nodes[id]
		if node == nil {
			break
		}
		if strings.ToLower(node.Role) == "table" {
			return node.ID
		}
		id = node.Parent
	}
	return model.ObjectID{}
}

func tableGrid(graph *model.Graph, tableID model.ObjectID) (map[[2]int]*model.Node, int, int) {
	grid := map[[2]int]*model.Node{}
	maxRow, maxColumn := 0, 0
	if table := graph.Nodes[tableID]; table != nil {
		maxRow, maxColumn = table.RowCount, table.ColumnCount
	}
	for _, id := range graph.Order {
		node := graph.Nodes[id]
		if !isTableCell(node) || node.Row < 1 || node.Column < 1 || containingTable(graph, node.ID) != tableID {
			continue
		}
		rowSpan, columnSpan := max(1, node.RowSpan), max(1, node.ColumnSpan)
		maxRow = max(maxRow, node.Row+rowSpan-1)
		maxColumn = max(maxColumn, node.Column+columnSpan-1)
		for row := node.Row; row < node.Row+rowSpan; row++ {
			for column := node.Column; column < node.Column+columnSpan; column++ {
				coordinate := [2]int{row, column}
				if grid[coordinate] == nil {
					grid[coordinate] = node
				}
			}
		}
	}
	return grid, maxRow, maxColumn
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
	var escapeNode *model.Node
	if command.ID == "escape" {
		e.cursor.Mode = "browse"
		if e.graph != nil {
			escapeNode = e.graph.Nodes[e.cursor.Object]
		}
	} else if command.ID == "toggleFocusMode" {
		if e.cursor.Mode == "browse" {
			e.cursor.Mode = "focus"
		} else {
			e.cursor.Mode = "browse"
		}
	}
	mode := e.cursor.Mode
	e.mu.Unlock()
	e.store.Append(events.Event{Kind: events.KindMode, SessionID: e.session(), CausalCommand: command.ID, Mode: mode, Provenance: events.ProvenanceScreenReaderEvent})
	if command.ID == "escape" && escapeNode != nil {
		e.emit(e.presenter.Present(escapeNode, "focus"), escapeNode, command.ID, "escape", "normal")
		return nil
	}
	e.emit(e.presenter.Mode(mode), nil, command.ID, "mode", "normal")
	return nil
}

func (e *Engine) report(command profile.Command) error {
	e.mu.RLock()
	graph := e.graph
	node := graph.Nodes[e.cursor.Object]
	cursorOffset := e.cursor.Offset
	focusNode := graph.Nodes[e.focus]
	e.mu.RUnlock()
	if node == nil {
		return errors.New("cursor node is unavailable")
	}
	switch command.ID {
	case "reportDetails":
		e.emit(e.presenter.Details(node.RelationText["details"]), node, command.ID, "reportDetails", "normal")
		return nil
	case "readCurrent":
		e.emit(e.presenter.Present(node, "report"), node, command.ID, "report", "normal")
		return nil
	case "reportTitle":
		documentID, _ := graph.DocumentRoot(node.ID)
		document := graph.Nodes[documentID]
		e.emit(e.presenter.Title(document), document, command.ID, "reportTitle", "normal")
		return nil
	case "reportShortcutKey":
		if focusNode == nil {
			focusNode = node
		}
		e.emit(e.presenter.ShortcutKey(focusNode.KeyboardShortcut), focusNode, command.ID, "reportShortcut", "normal")
		return nil
	case "reportCurrentLine":
		line := lineAt(node, cursorOffset)
		e.emit(e.presenter.CurrentLine(line), node, command.ID, "reportLine", "normal")
		return nil
	case "reportTextSelection":
		selectionNode := focusNode
		if selectionNode == nil || !selectionNode.HasInterface("org.a11y.atspi.Text") {
			selectionNode = node
		}
		selection := selectedText(selectionNode)
		e.emit(e.presenter.Selection(selection), selectionNode, command.ID, "reportSelection", "normal")
		return nil
	case "reportTextFormatting":
		e.emit(e.presenter.TextFormatting(node, cursorOffset), node, command.ID, "reportFormatting", "normal")
		return nil
	case "reportLanguage":
		language := node.Locale
		for _, run := range node.TextAttributeRuns {
			if cursorOffset >= run.Start && cursorOffset < run.End && strings.TrimSpace(run.Attributes["language"]) != "" {
				language = run.Attributes["language"]
				break
			}
		}
		e.emit(e.presenter.Language(language), node, command.ID, "reportLanguage", "normal")
		return nil
	case "reportLinkDestination":
		e.emit(e.presenter.LinkDestination(node.Attributes["url"]), node, command.ID, "reportLinkDestination", "normal")
		return nil
	case "reportCaretLocation":
		e.emit(e.presenter.CaretLocation(node.Bounds), node, command.ID, "reportCaretLocation", "normal")
		return nil
	case "readActiveWindow":
		items := make([]*model.Node, 0, len(graph.Order))
		for _, id := range graph.Order {
			items = append(items, graph.Nodes[id])
		}
		return e.readItems(command.ID, "activeWindow", items)
	case "sayAll":
		// Continue below with document-scoped items from the browse cursor.
	default:
		return fmt.Errorf("unsupported report command %q", command.ID)
	}
	e.mu.RLock()
	start := graph.Index(node.ID)
	document, scoped := graph.DocumentRoot(node.ID)
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
	return e.readItems(command.ID, "sayAll", items)
}

func (e *Engine) readItems(commandID, reason string, items []*model.Node) error {
	spoken := make([]string, 0, len(items))
	var firstSpeech events.Event
	for _, item := range items {
		presentation := e.presenter.Present(item, reason)
		event := e.emitEvidence(presentation, item, commandID, reason, "normal", false)
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

func lineAt(node *model.Node, offset int) string {
	if node == nil {
		return ""
	}
	text := navigableText(node)
	if text == "" {
		return node.SpokenContent()
	}
	ranges, err := model.TextUnitRanges(node.ID, text, model.TextUnitLine)
	if err != nil || len(ranges) == 0 {
		return text
	}
	for _, value := range ranges {
		if offset >= value.Start && offset <= value.End {
			line, lineErr := value.Text(text)
			if lineErr == nil {
				return line
			}
		}
	}
	line, err := ranges[len(ranges)-1].Text(text)
	if err != nil {
		return text
	}
	return line
}

func selectedText(node *model.Node) string {
	if node == nil {
		return ""
	}
	text := navigableText(node)
	parts := make([]string, 0, len(node.Selections))
	for _, selection := range node.Selections {
		value, err := selection.Text(text)
		if err == nil && strings.TrimSpace(value) != "" {
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " ")
}

func (e *Engine) reportTableAxis(command profile.Command) error {
	e.mu.Lock()
	graph := e.graph
	current := containingTableCell(graph, e.cursor.Object)
	if current == nil {
		e.mu.Unlock()
		return errors.New("cursor is not inside a table cell")
	}
	tableID := containingTable(graph, current.ID)
	grid, maxRow, maxColumn := tableGrid(graph, tableID)
	start, end := 1, maxColumn
	if command.Target == "column" {
		end = maxRow
	}
	if strings.HasPrefix(command.ID, "sayAll") {
		if command.Target == "row" {
			start = current.Column
		} else {
			start = current.Row
		}
	}
	items := make([]*model.Node, 0, max(0, end-start+1))
	seen := make(map[model.ObjectID]bool)
	for position := start; position <= end; position++ {
		coordinate := [2]int{current.Row, position}
		if command.Target == "column" {
			coordinate = [2]int{position, current.Column}
		}
		cell := grid[coordinate]
		if cell != nil && !seen[cell.ID] {
			seen[cell.ID] = true
			items = append(items, cell)
		}
	}
	if len(items) == 0 {
		e.mu.Unlock()
		return errors.New("table axis has no readable cells")
	}
	if strings.HasPrefix(command.ID, "sayAll") {
		e.cursor.Object, e.cursor.Offset = items[len(items)-1].ID, 0
	}
	e.mu.Unlock()
	spoken := make([]string, 0, len(items))
	var firstSpeech events.Event
	var previous *model.Node
	for _, item := range items {
		presentation := e.presenter.PresentTableMove(item, previous)
		event := e.emitEvidence(presentation, item, command.ID, "tableNavigation", "normal", false)
		if firstSpeech.Sequence == 0 && event.Sequence != 0 {
			firstSpeech = event
		}
		if presentation.Speech != "" {
			spoken = append(spoken, presentation.Speech)
		}
		previous = item
	}
	if firstSpeech.Sequence != 0 && len(spoken) > 0 {
		e.startSynthesis(firstSpeech, profile.Presentation{Speech: strings.Join(spoken, ". ")})
	}
	return nil
}

func (e *Engine) dialog(command profile.Command, argument string) error {
	switch command.ID {
	case "elementsList":
		return e.reportElementsList(command.ID)
	case "find":
		query := strings.TrimSpace(argument)
		if query == "" {
			return errors.New("find requires a non-empty query through the structured action API")
		}
		e.mu.Lock()
		e.findQuery = query
		e.mu.Unlock()
		return e.find(command.ID, query, 1)
	case "findNext", "findPrevious":
		e.mu.RLock()
		query := e.findQuery
		e.mu.RUnlock()
		if query == "" {
			return errors.New("no previous find query")
		}
		direction := 1
		if command.ID == "findPrevious" {
			direction = -1
		}
		return e.find(command.ID, query, direction)
	default:
		return fmt.Errorf("unsupported dialog command %q", command.ID)
	}
}

func (e *Engine) find(commandID, query string, direction int) error {
	e.mu.Lock()
	graph, current := e.graph, e.cursor.Object
	if graph == nil {
		e.mu.Unlock()
		return errors.New("accessible graph is unavailable")
	}
	document, scoped := graph.DocumentRoot(current)
	start := graph.Index(current)
	queryFold := strings.ToLower(query)
	var match *model.Node
	for index := start + direction; index >= 0 && index < len(graph.Order); index += direction {
		node := graph.Nodes[graph.Order[index]]
		if node == nil || (scoped && !graph.InDocument(node.ID, document)) {
			continue
		}
		haystack := strings.Join([]string{node.SpokenContent(), node.Description}, " ")
		if !node.IsProtected() {
			haystack += " " + node.Text
		}
		if strings.Contains(strings.ToLower(haystack), queryFold) {
			if sameFindOccurrence(graph, current, node, queryFold) {
				continue
			}
			match = node
			e.cursor.Object, e.cursor.Offset = node.ID, 0
			break
		}
	}
	e.mu.Unlock()
	if match == nil {
		e.emit(e.presenter.NoTarget("text "+query, direction), nil, commandID, "navigationBoundary", "normal")
		return nil
	}
	e.emit(e.presenter.Present(match, "textNavigation"), match, commandID, "find", "normal")
	return nil
}

func sameFindOccurrence(graph *model.Graph, current model.ObjectID, candidate *model.Node, queryFold string) bool {
	if graph == nil || candidate == nil || current == candidate.ID {
		return current == candidate.ID
	}
	currentNode := graph.Nodes[current]
	if currentNode == nil || (!isGraphAncestor(graph, current, candidate.ID) && !isGraphAncestor(graph, candidate.ID, current)) {
		return false
	}
	currentText := strings.ToLower(strings.Join([]string{currentNode.SpokenContent(), currentNode.Description, currentNode.Text}, " "))
	candidateText := strings.ToLower(strings.Join([]string{candidate.SpokenContent(), candidate.Description, candidate.Text}, " "))
	return strings.Contains(currentText, queryFold) && strings.Contains(candidateText, queryFold) &&
		normalizeFindOccurrence(currentText) == normalizeFindOccurrence(candidateText)
}

func normalizeFindOccurrence(value string) string {
	return strings.Join(strings.FieldsFunc(value, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}), " ")
}

func isGraphAncestor(graph *model.Graph, ancestor, descendant model.ObjectID) bool {
	for id, steps := descendant, 0; graph != nil && id.Valid() && steps < 512; steps++ {
		node := graph.Nodes[id]
		if node == nil || !node.Parent.Valid() {
			return false
		}
		if node.Parent == ancestor {
			return true
		}
		id = node.Parent
	}
	return false
}

func (e *Engine) reportElementsList(commandID string) error {
	e.mu.RLock()
	graph, current := e.graph, e.cursor.Object
	e.mu.RUnlock()
	if graph == nil {
		return errors.New("accessible graph is unavailable")
	}
	document, scoped := graph.DocumentRoot(current)
	counts := map[string]int{"headings": 0, "links": 0, "form fields": 0, "buttons": 0, "landmarks": 0}
	for _, id := range graph.Order {
		if scoped && !graph.InDocument(id, document) {
			continue
		}
		node := graph.Nodes[id]
		if node == nil {
			continue
		}
		for label, target := range map[string]string{
			"headings": "heading", "links": "link", "form fields": "formField", "buttons": "button", "landmarks": "landmark",
		} {
			if profile.MatchTarget(target)(node) {
				counts[label]++
			}
		}
	}
	text := fmt.Sprintf("Elements list  %d links  %d headings  %d form fields  %d buttons  %d landmarks",
		counts["links"], counts["headings"], counts["form fields"], counts["buttons"], counts["landmarks"])
	braille := fmt.Sprintf("Elements list %d lnk %d hdg %d form %d btn %d lmk",
		counts["links"], counts["headings"], counts["form fields"], counts["buttons"], counts["landmarks"])
	e.emit(profile.Presentation{Speech: text, Braille: braille}, nil, commandID, "elementsList", "normal")
	return nil
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
	redacted := node != nil && node.IsProtected()
	var speechEvent events.Event
	if presentation.Speech != "" {
		speechEvent = e.store.Append(events.Event{Kind: events.KindSpeech, SessionID: sessionID, CausalCommand: command, Source: source, Text: presentation.Speech, SpeechCommands: presentation.SpeechCommands, Reason: reason, Priority: priority, Provenance: events.ProvenanceScreenReaderOutput, Redacted: redacted})
	}
	if presentation.Braille != "" {
		translationContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		translation, err := e.braille.Translate(translationContext, presentation.Braille, e.cfg.Locale, 0)
		cancel()
		if err != nil {
			e.logger.Error("braille translation failed", "error", err)
			translation = braille.Result{Text: presentation.Braille, Cells: []byte(presentation.Braille)}
		}
		// NVDA's braille spy exposes the logical braille buffer text separately
		// from the translated cell array written to a display. Keep the same
		// boundary: Text is the presenter-authored display line, while
		// BrailleCells contains the locale-table translation. Publishing the
		// Liblouis textual rendering here used to make assertions depend on the
		// lou_translate output format instead of the screen-reader buffer.
		e.store.Append(events.Event{Kind: events.KindBraille, SessionID: sessionID, CausalCommand: command, Source: source, Text: presentation.Braille, BrailleCells: translation.Cells, BrailleCursor: translation.Cursor, Reason: reason, Provenance: events.ProvenanceScreenReaderOutput, Redacted: redacted})
	}
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
		e.store.Append(events.Event{Kind: events.KindAudio, SessionID: event.SessionID, CausalCommand: event.CausalCommand, Source: event.Source, AudioOffsetNS: event.MonotonicNS, AudioDurationNS: audio.Duration.Nanoseconds(), Reason: "synthesized", Provenance: events.ProvenanceSynthesizedAudio, Redacted: event.Redacted})
	}()
}

func (e *Engine) eventLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case native, ok := <-e.access.Events():
			if !ok {
				return
			}
			if strings.Contains(native.Name, ".Document.") {
				e.mu.Lock()
				e.focus = native.Source
				e.browserWindowActive = true
				e.webContentFocused = true
				e.graphDirty = true
				e.mu.Unlock()
			} else if strings.Contains(native.Name, ".Object.") {
				e.mu.Lock()
				e.graphDirty = true
				e.mu.Unlock()
			}
			if strings.HasSuffix(native.Name, ".Focus") || (strings.HasSuffix(native.Name, ".StateChanged") && native.Detail == "focused" && native.Detail1 != 0) {
				if !e.handleFocus(ctx, native.Source) {
					continue
				}
			}
			if strings.HasSuffix(native.Name, ".StateChanged") {
				e.handleStateChange(ctx, native)
				continue
			}
			if strings.HasSuffix(native.Name, ".Announcement") {
				text, _ := native.Value.(string)
				if text != "" {
					node, _ := e.access.ReadNode(ctx, native.Source)
					e.emitLiveRegion(profile.Presentation{Speech: text, Braille: text}, node, native.Source, livePriority(native.Detail1))
				}
				continue
			}
			if strings.HasSuffix(native.Name, ".TextChanged") {
				e.handleLiveTextChange(ctx, native)
				continue
			}
			if strings.HasSuffix(native.Name, ".ChildrenChanged") {
				e.handleLiveChildrenChange(ctx, native)
				continue
			}
			if strings.HasSuffix(native.Name, ".PropertyChange") && (native.Detail == "accessible-name" || native.Detail == "accessible-description") {
				e.handleLivePropertyChange(ctx, native)
			}
		}
	}
}

func (e *Engine) handleStateChange(ctx context.Context, native NativeEvent) {
	state := strings.ToLower(strings.TrimSpace(native.Detail))
	if !slices.Contains([]string{"checked", "indeterminate", "selected", "pressed", "expanded"}, state) {
		return
	}
	e.mu.RLock()
	focused := e.focus == native.Source
	e.mu.RUnlock()
	if !focused {
		return
	}
	node, err := e.access.ReadNode(ctx, native.Source)
	if err != nil || node == nil {
		return
	}
	if node.States == nil {
		node.States = map[string]bool{}
	}
	node.States[state] = native.Detail1 != 0
	e.emit(e.presenter.Present(node, "focus"), node, e.causalEventCommand(), "nativeStateChange", "normal")
}

type liveRegionMetadata struct {
	priority       string
	atomic         bool
	relevant       map[string]bool
	container      *model.Node
	containerOwned bool
}

func (e *Engine) liveRegionMetadata(node *model.Node) (liveRegionMetadata, bool) {
	e.mu.RLock()
	graph := e.graph
	e.mu.RUnlock()
	metadata := liveRegionMetadata{relevant: map[string]bool{"additions": true, "text": true}}
	var inheritedPriority string
	var inheritedContainer *model.Node
	for steps := 0; node != nil && steps < 512; steps++ {
		if strings.EqualFold(node.Attributes["busy"], "true") || strings.EqualFold(node.Attributes["container-busy"], "true") || node.HasState("busy") {
			return liveRegionMetadata{}, false
		}
		if strings.EqualFold(node.Attributes["atomic"], "true") || strings.EqualFold(node.Attributes["container-atomic"], "true") {
			metadata.atomic = true
		}
		// Chromium exposes a node's default relevant value alongside the
		// inherited live-container value. The latter governs descendant events.
		if value := strings.TrimSpace(node.Attributes["container-relevant"]); value != "" {
			metadata.relevant = liveRelevant(value)
		} else if value = strings.TrimSpace(node.Attributes["relevant"]); value != "" {
			metadata.relevant = liveRelevant(value)
		}
		switch value := strings.ToLower(strings.TrimSpace(node.Attributes["live"])); value {
		case "off":
			return liveRegionMetadata{}, false
		case "assertive", "polite":
			metadata.priority, metadata.container, metadata.containerOwned = value, node, true
			return metadata, true
		}
		switch value := strings.ToLower(strings.TrimSpace(node.Attributes["container-live"])); value {
		case "off":
			return liveRegionMetadata{}, false
		case "assertive", "polite":
			// Chromium exposes inherited live-region attributes on descendants.
			// Keep walking the cached accessibility graph so atomic updates can
			// resolve and read the actual live-region container.
			if inheritedPriority == "" {
				inheritedPriority, inheritedContainer = value, node
			}
		}
		role := strings.ToLower(node.Role)
		if role == "alert" {
			metadata.priority, metadata.container, metadata.containerOwned = "assertive", node, true
			return metadata, true
		}
		if role == "statusbar" || role == "status bar" || role == "status" || role == "log" {
			metadata.priority, metadata.container, metadata.containerOwned = "polite", node, true
			return metadata, true
		}
		if graph == nil {
			break
		}
		parent := node.Parent
		if cached := graph.Nodes[node.ID]; !parent.Valid() && cached != nil {
			parent = cached.Parent
		}
		if !parent.Valid() && inheritedPriority != "" {
			// A direct AT-SPI object read has no Parent. New live-region text
			// objects can also arrive before the cached graph contains their ID.
			// Chromium exposes the owning live container through MEMBER_OF in
			// that window; use only a target already present in the graph so an
			// unrelated relation cannot escape the active document.
			for _, candidate := range node.Relations["member of"] {
				if candidate.Valid() && graph.Nodes[candidate] != nil {
					parent = candidate
					break
				}
			}
		}
		if !parent.Valid() {
			break
		}
		node = graph.Nodes[parent]
	}
	if inheritedPriority != "" {
		metadata.priority, metadata.container = inheritedPriority, inheritedContainer
		return metadata, true
	}
	return liveRegionMetadata{}, false
}

func liveRelevant(value string) map[string]bool {
	result := map[string]bool{}
	for _, token := range strings.Fields(strings.ToLower(value)) {
		if token == "all" {
			return map[string]bool{"additions": true, "removals": true, "text": true}
		}
		if token == "additions" || token == "removals" || token == "text" {
			result[token] = true
		}
	}
	return result
}

func (e *Engine) handleLiveTextChange(ctx context.Context, native NativeEvent) {
	node, err := e.access.ReadNode(ctx, native.Source)
	if err != nil {
		return
	}
	metadata, live := e.liveRegionMetadata(node)
	if !live {
		return
	}
	// Chromium also reports a newly inserted accessible subtree through
	// TextChanged. Treat its first unknown object as an addition, then remember
	// the subtree so later character edits obey aria-relevant=text.
	e.mu.RLock()
	known := e.liveRegionKnown[native.Source]
	e.mu.RUnlock()
	containerInsertion := metadata.containerOwned && metadata.container != nil && native.Source == metadata.container.ID
	addition := strings.Contains(strings.ToLower(native.Detail), "insert") && metadata.relevant["additions"] && (!known || containerInsertion)
	e.markLiveRegionSubtreeKnown(ctx, native.Source)
	if !metadata.relevant["text"] && !addition {
		return
	}
	text, _ := native.Value.(string)
	if containerInsertion && metadata.container != nil {
		if current, content := e.readLiveRegionContent(ctx, metadata.container); current != nil {
			metadata.container, node, text = current, current, content
		}
	}
	if metadata.atomic && metadata.container != nil {
		if current, content := e.readLiveRegionContent(ctx, metadata.container); current != nil {
			metadata.container, node, text = current, current, content
		}
	}
	if text != "" {
		e.emitLiveRegion(profile.Presentation{Speech: text, Braille: text}, node, native.Source, metadata.priority)
	}
}

func (e *Engine) handleLiveChildrenChange(ctx context.Context, native NativeEvent) {
	node, err := e.access.ReadNode(ctx, native.Source)
	if err != nil {
		return
	}
	metadata, live := e.liveRegionMetadata(node)
	if !live {
		return
	}
	relevance := "additions"
	if strings.Contains(strings.ToLower(native.Detail), "remove") {
		relevance = "removals"
	}
	if !metadata.relevant[relevance] {
		return
	}
	if relevance == "additions" {
		added := native.ValueObject
		if !added.Valid() {
			added = native.Source
		}
		e.markLiveRegionSubtreeKnown(ctx, added)
	}
	changed := node
	if native.ValueObject.Valid() {
		if current, readErr := e.access.ReadNode(ctx, native.ValueObject); readErr == nil && current != nil {
			changed = current
		}
	}
	text := changed.SpokenContent()
	node = changed
	if metadata.atomic && metadata.container != nil {
		if current, content := e.readLiveRegionContent(ctx, metadata.container); current != nil {
			text, node = content, current
		}
	}
	if text != "" {
		e.emitLiveRegion(profile.Presentation{Speech: text, Braille: text}, node, native.Source, metadata.priority)
	}
}

func (e *Engine) markLiveRegionSubtreeKnown(ctx context.Context, root model.ObjectID) {
	if !root.Valid() {
		return
	}
	known := make([]model.ObjectID, 0, 8)
	queue := []model.ObjectID{root}
	seen := make(map[model.ObjectID]bool, 8)
	for len(queue) > 0 && len(seen) < 1024 {
		id := queue[0]
		queue = queue[1:]
		if !id.Valid() || seen[id] {
			continue
		}
		seen[id] = true
		known = append(known, id)
		node, err := e.access.ReadNode(ctx, id)
		if err == nil && node != nil {
			queue = append(queue, node.Children...)
		}
	}
	e.mu.Lock()
	if e.liveRegionKnown == nil {
		e.liveRegionKnown = make(map[model.ObjectID]bool, len(known))
	}
	for _, id := range known {
		e.liveRegionKnown[id] = true
	}
	e.mu.Unlock()
}

func (e *Engine) handleLivePropertyChange(ctx context.Context, native NativeEvent) {
	node, err := e.access.ReadNode(ctx, native.Source)
	if err != nil {
		return
	}
	metadata, live := e.liveRegionMetadata(node)
	if !live || !metadata.relevant["text"] {
		return
	}
	text, _ := native.Value.(string)
	if metadata.atomic && metadata.container != nil {
		if current, content := e.readLiveRegionContent(ctx, metadata.container); current != nil {
			text, node = content, current
		}
	}
	if text != "" {
		e.emitLiveRegion(profile.Presentation{Speech: text, Braille: text}, node, native.Source, metadata.priority)
	}
}

func (e *Engine) readLiveRegionContent(ctx context.Context, container *model.Node) (*model.Node, string) {
	if container == nil || !container.ID.Valid() {
		return nil, ""
	}
	root, err := e.access.ReadNode(ctx, container.ID)
	if err != nil || root == nil {
		return nil, ""
	}
	queue := []*model.Node{root}
	seen := make(map[model.ObjectID]bool, 32)
	parts := make([]string, 0, 8)
	for len(queue) > 0 && len(seen) < 1024 {
		node := queue[0]
		queue = queue[1:]
		if node == nil || seen[node.ID] {
			continue
		}
		seen[node.ID] = true
		value := strings.TrimSpace(strings.ReplaceAll(node.SpokenContent(), "\ufffc", ""))
		if node.IsProtected() {
			value = "[redacted]"
		}
		if value != "" {
			redundant := false
			for _, existing := range parts {
				if existing == value || strings.Contains(existing, value) {
					redundant = true
					break
				}
			}
			if !redundant {
				parts = append(parts, value)
			}
		}
		for _, childID := range node.Children {
			if seen[childID] {
				continue
			}
			child, readErr := e.access.ReadNode(ctx, childID)
			if readErr == nil && child != nil {
				queue = append(queue, child)
			}
		}
	}
	return root, strings.Join(parts, " ")
}

func (e *Engine) emitLiveRegion(presentation profile.Presentation, node *model.Node, nativeSource model.ObjectID, priority string) {
	if !e.presenter.Settings().ReportDynamicContentChanges {
		return
	}
	if node != nil && node.IsProtected() {
		presentation.Speech = "[redacted]"
		presentation.Braille = "[redacted]"
	}
	now := time.Now()
	e.mu.Lock()
	// Chromium commonly exposes one DOM mutation as TextChanged followed by a
	// delayed ChildrenChanged signal. Track each recent announcement rather than
	// only the last one: assertive and polite transport signals can interleave.
	const duplicateWindow = 750 * time.Millisecond
	const interruptionWindow = 100 * time.Millisecond
	key := liveRegionAnnouncement{source: nativeSource, text: presentation.Speech, priority: priority}
	for candidate, emittedAt := range e.recentLiveRegions {
		if now.Sub(emittedAt) >= duplicateWindow {
			delete(e.recentLiveRegions, candidate)
			continue
		}
		// AT-SPI may deliver the assertive mutation before an earlier polite
		// sibling mutation. Match screen-reader interruption semantics by dropping
		// that immediately trailing polite announcement.
		if priority == "polite" && candidate.priority == "assertive" && now.Sub(emittedAt) < interruptionWindow {
			e.mu.Unlock()
			return
		}
	}
	if emittedAt, exists := e.recentLiveRegions[key]; exists && now.Sub(emittedAt) < duplicateWindow {
		e.mu.Unlock()
		return
	}
	if e.recentLiveRegions == nil {
		e.recentLiveRegions = make(map[liveRegionAnnouncement]time.Time)
	}
	e.recentLiveRegions[key] = now
	e.mu.Unlock()
	var source *model.ObjectID
	if nativeSource.Valid() {
		id := nativeSource
		source = &id
	} else if node != nil {
		id := node.ID
		source = &id
	}
	command := e.causalEventCommand()
	redacted := node != nil && node.IsProtected()
	e.store.Append(events.Event{Kind: events.KindLiveRegion, SessionID: e.session(), CausalCommand: command, Source: source, Text: presentation.Speech, Reason: "liveRegion", Priority: priority, Provenance: events.ProvenanceAccessibilityEvent, Redacted: redacted})
	e.emit(presentation, node, command, "liveRegion", priority)
}

func (e *Engine) handleFocus(ctx context.Context, id model.ObjectID) bool {
	node, err := e.access.ReadNode(ctx, id)
	if err != nil {
		e.logger.Debug("read focus object", "error", err)
		return false
	}
	e.mu.RLock()
	known := e.graph != nil && e.graph.Nodes[id] != nil
	knownWebContent := known && hasDocumentAncestor(e.graph, id)
	e.mu.RUnlock()
	if !known {
		// Focus signals include browser chrome and can race application teardown.
		// Never let this event-loop refresh inherit the process-lifetime context:
		// a vanished preferred object would otherwise retain refreshMu forever.
		refreshCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		refreshErr := e.Refresh(refreshCtx)
		cancel()
		if refreshErr != nil {
			e.logger.Debug("refresh graph for new focus object", "error", refreshErr)
		} else {
			if refreshed, readErr := e.access.ReadNode(ctx, id); readErr == nil {
				node = refreshed
			}
			e.mu.RLock()
			known = e.graph != nil && e.graph.Nodes[id] != nil
			knownWebContent = known && hasDocumentAncestor(e.graph, id)
			e.mu.RUnlock()
		}
	}
	isDocument := node.Role == "document web" || node.Role == "document frame"
	e.mu.Lock()
	if e.graph != nil {
		if existing := e.graph.Nodes[id]; existing != nil {
			node.RowHeaderText = append([]string(nil), existing.RowHeaderText...)
			node.ColumnHeaderText = append([]string(nil), existing.ColumnHeaderText...)
			node.RelationText = cloneRelationText(existing.RelationText)
		}
	}
	e.focus = id
	e.navigator = id
	if e.reviewCopySet && e.reviewCopyStart.Object != id {
		e.reviewCopySet = false
		e.reviewSelectionSet = false
	}
	reviewOffset := max(node.CaretOffset, 0)
	if count := len([]rune(navigableText(node))); count > 0 {
		reviewOffset = min(reviewOffset, count-1)
	} else {
		reviewOffset = 0
	}
	e.review = model.Cursor{Object: id, Offset: reviewOffset, Mode: "object"}
	e.brailleOffset = 0
	e.browserWindowActive = true
	e.webContentFocused = isDocument || knownWebContent || (!known && nodeLooksLikeWebContent(node))
	if !known {
		// A focus event can arrive for a newly created DOM node or for browser
		// chrome. Keep it stale when the bounded refresh could not incorporate
		// the object; the next graph-dependent command or export retries with its
		// own request deadline.
		e.graphDirty = true
	}
	oldMode := e.cursor.Mode
	if isDocument || !e.webContentFocused {
		e.cursor.Mode = "browse"
	} else if focusModeTarget(e.graph, node) {
		e.cursor.Mode = "focus"
	} else {
		e.cursor.Mode = "browse"
	}
	mode, modeChanged := e.cursor.Mode, oldMode != e.cursor.Mode
	if isDocument {
		e.graphDirty = true
	}
	if e.webContentFocused && (isDocument || e.cursor.Mode == "focus" || node.HasState("focusable")) {
		e.cursor.Object, e.cursor.Offset = id, 0
	}
	e.mu.Unlock()
	command := e.causalFocusCommand()
	e.store.Append(events.Event{Kind: events.KindFocus, SessionID: e.session(), CausalCommand: command, Source: &id, Text: node.Name, Reason: "nativeFocus", Provenance: events.ProvenanceAccessibilityEvent, Redacted: node.IsProtected()})
	if modeChanged {
		e.store.Append(events.Event{Kind: events.KindMode, SessionID: e.session(), CausalCommand: command, Source: &id, Mode: mode, Reason: "automaticFocusMode", Provenance: events.ProvenanceScreenReaderEvent})
	}
	e.emit(e.presenter.Present(node, "focus"), node, command, "nativeFocus", "normal")
	return isDocument
}

func nodeLooksLikeWebContent(node *model.Node) bool {
	if node == nil {
		return false
	}
	return strings.TrimSpace(node.Attributes["tag"]) != "" ||
		strings.TrimSpace(node.Attributes["xml-roles"]) != "" ||
		strings.TrimSpace(node.Attributes["display"]) != ""
}

func focusModeTarget(graph *model.Graph, node *model.Node) bool {
	if node == nil {
		return false
	}
	role := strings.ToLower(node.Role)
	if node.HasState("editable") || slices.Contains([]string{
		"entry", "password text", "combo box", "list box", "tree", "tree table", "spin button", "slider",
	}, role) {
		return true
	}
	for id, steps := node.Parent, 0; graph != nil && id.Valid() && steps < 512; id, steps = graph.Nodes[id].Parent, steps+1 {
		ancestor := graph.Nodes[id]
		if ancestor == nil {
			break
		}
		if strings.ToLower(ancestor.Role) == "application" || strings.Contains(ancestor.Attributes["xml-roles"], "application") {
			return true
		}
	}
	return false
}

func cloneRelationText(source map[string][]string) map[string][]string {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string][]string, len(source))
	for relation, values := range source {
		result[relation] = append([]string(nil), values...)
	}
	return result
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
