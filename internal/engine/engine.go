package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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
	Locale         string
	KeyboardLayout string
	BrowserProcess string
	StartupTimeout time.Duration
	SynthRequest   synth.Request
}

type State struct {
	Ready                  bool           `json:"ready"`
	GraphRevision          uint64         `json:"graphRevision"`
	Cursor                 model.Cursor   `json:"cursor"`
	CursorInDocument       bool           `json:"cursorInDocument"`
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
	findQuery           string
	synthCancel         context.CancelFunc
	synthDone           <-chan struct{}
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
	e.graphDirty = e.webContentFocused && e.focus != focusAtStart
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
	cursorInDocument := false
	if e.graph != nil {
		revision = e.graph.Revision
		_, cursorInDocument = e.graph.DocumentRoot(e.cursor.Object)
	}
	return State{Ready: e.ready, GraphRevision: revision, Cursor: e.cursor, CursorInDocument: cursorInDocument, Focus: e.focus, BrowserWindowActive: e.browserWindowActive, WebContentFocused: e.webContentFocused, SingleLetterNavigation: e.singleLetter, LastSequence: e.store.Cursor()}
}

func (e *Engine) DocumentSnapshot() []model.Node {
	e.mu.RLock()
	defer e.mu.RUnlock()
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
	consume, err := e.execute(ctx, command, "")
	return consume, err
}

func (e *Engine) ExecuteDirect(ctx context.Context, commandID string) error {
	command, ok := profile.CommandByID(commandID)
	if !ok {
		return fmt.Errorf("unsupported command %q", commandID)
	}
	_, err := e.execute(ctx, command, "")
	return err
}

func (e *Engine) ExecuteDirectWithArgument(ctx context.Context, commandID, argument string) error {
	command, ok := profile.CommandByID(commandID)
	if !ok {
		return fmt.Errorf("unsupported command %q", commandID)
	}
	_, err := e.execute(ctx, command, argument)
	return err
}

func (e *Engine) execute(ctx context.Context, command profile.Command, argument string) (bool, error) {
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
		err = e.dialog(command, argument)
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
	case "quickNavigation", "text", "activation", "report", "table", "dialog":
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
	var table, tableCell *model.Node
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
	e.mu.Unlock()
	if !ok {
		target := command.Target
		if target == "textParagraph" {
			target = "text paragraph"
		}
		e.emit(e.presenter.NoTarget(target, command.Direction), nil, command.ID, "navigationBoundary", "normal")
		return nil
	}
	presentation := e.presenter.Present(node, "quickNavigation")
	switch command.Target {
	case "textParagraph":
		presentation = e.presenter.PresentTextParagraph(node)
	case "table":
		presentation = e.presenter.PresentTableEntry(table, tableCell)
	}
	e.emit(presentation, node, command.ID, "quickNavigation", "normal")
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
		e.emit(e.presenter.Present(node, "textNavigation"), node, command.ID, "textNavigation", "normal")
		return nil
	} else if command.ID == "nextParagraphText" || command.ID == "previousParagraphText" {
		next, ok := graph.MoveInDocument(e.cursor.Object, command.Direction, profile.MatchTarget("textParagraph"))
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
	if command.ID == "reportDetails" {
		e.emit(e.presenter.Details(node.RelationText["details"]), node, command.ID, "reportDetails", "normal")
		return nil
	}
	if command.ID != "sayAll" {
		e.emit(e.presenter.Present(node, "report"), node, command.ID, "report", "normal")
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
		haystack := strings.Join([]string{node.SpokenContent(), node.Description, node.Text}, " ")
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
	speechEvent := e.store.Append(events.Event{Kind: events.KindSpeech, SessionID: sessionID, CausalCommand: command, Source: source, Text: presentation.Speech, SpeechCommands: presentation.SpeechCommands, Reason: reason, Priority: priority})
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
	e.store.Append(events.Event{Kind: events.KindBraille, SessionID: sessionID, CausalCommand: command, Source: source, Text: presentation.Braille, BrailleCells: translation.Cells, BrailleCursor: translation.Cursor, Reason: reason})
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
	e.mu.RLock()
	known := e.graph != nil && e.graph.Nodes[id] != nil
	e.mu.RUnlock()
	if !known {
		if err := e.Refresh(ctx); err != nil {
			e.logger.Debug("refresh graph for new focus object", "error", err)
		} else if refreshed, err := e.access.ReadNode(ctx, id); err == nil {
			node = refreshed
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
	e.browserWindowActive = true
	e.webContentFocused = isDocument || hasDocumentAncestor(e.graph, id)
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
	e.store.Append(events.Event{Kind: events.KindFocus, SessionID: e.session(), Source: &id, Text: node.Name, Reason: "nativeFocus"})
	if modeChanged {
		e.store.Append(events.Event{Kind: events.KindMode, SessionID: e.session(), CausalCommand: "focus", Source: &id, Mode: mode, Reason: "automaticFocusMode"})
	}
	e.emit(e.presenter.Present(node, "focus"), node, "focus", "nativeFocus", "normal")
	return isDocument
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
