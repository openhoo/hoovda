package atspi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/openhoo/hoovda/internal/model"
)

type Client struct {
	conn      *dbus.Conn
	logger    *slog.Logger
	signals   chan *dbus.Signal
	events    chan NativeEvent
	closed    chan struct{}
	closeOnce sync.Once
	revision  atomic.Uint64
}

func Connect(ctx context.Context, logger *slog.Logger) (*Client, error) {
	session, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("connect session bus: %w", err)
	}
	var address string
	call := session.Object("org.a11y.Bus", "/org/a11y/bus").CallWithContext(ctx, "org.a11y.Bus.GetAddress", 0)
	if err := call.Store(&address); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("discover accessibility bus: %w", err)
	}
	_ = session.Close()
	conn, err := dbus.Connect(address)
	if err != nil {
		return nil, fmt.Errorf("connect accessibility bus: %w", err)
	}
	client := &Client{conn: conn, logger: logger, signals: make(chan *dbus.Signal, 1024), events: make(chan NativeEvent, 1024), closed: make(chan struct{})}
	if err := client.registerEvents(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	conn.Signal(client.signals)
	go client.signalLoop()
	return client, nil
}

func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		c.conn.RemoveSignal(c.signals)
		err = c.conn.Close()
	})
	return err
}

func (c *Client) Events() <-chan NativeEvent { return c.events }

func (c *Client) registerEvents(ctx context.Context) error {
	registry := c.conn.Object(BusName, RegistryPath)
	for _, eventType := range []string{"object:", "focus:", "document:", "window:"} {
		if call := registry.CallWithContext(ctx, InterfaceRegistry+".RegisterEvent", 0, eventType, []string{}, ""); call.Err != nil {
			return fmt.Errorf("register AT-SPI event %s: %w", eventType, call.Err)
		}
	}
	for _, iface := range []string{"org.a11y.atspi.Event.Object", "org.a11y.atspi.Event.Focus", "org.a11y.atspi.Event.Document", "org.a11y.atspi.Event.Window"} {
		if err := c.conn.AddMatchSignalContext(ctx, dbus.WithMatchInterface(iface)); err != nil {
			return fmt.Errorf("match AT-SPI interface %s: %w", iface, err)
		}
	}
	return nil
}

func (c *Client) signalLoop() {
	defer close(c.events)
	for {
		select {
		case <-c.closed:
			return
		case signal, ok := <-c.signals:
			if !ok {
				return
			}
			event, ok := decodeSignal(signal)
			if !ok {
				continue
			}
			select {
			case c.events <- event:
			default:
				c.logger.Error("AT-SPI event channel overflow", "event", event.Name)
			}
		}
	}
}

func decodeSignal(signal *dbus.Signal) (NativeEvent, bool) {
	if signal == nil || !strings.HasPrefix(signal.Name, "org.a11y.atspi.Event.") || len(signal.Body) < 4 {
		return NativeEvent{}, false
	}
	detail, _ := signal.Body[0].(string)
	detail1, _ := signal.Body[1].(int32)
	detail2, _ := signal.Body[2].(int32)
	value := signal.Body[3]
	if variant, ok := value.(dbus.Variant); ok {
		value = variant.Value()
	}
	properties := map[string]dbus.Variant{}
	if len(signal.Body) > 4 {
		if typed, ok := signal.Body[4].(map[string]dbus.Variant); ok {
			properties = typed
		}
	}
	return NativeEvent{Name: signal.Name, Source: model.ObjectID{Bus: signal.Sender, Path: string(signal.Path)}, Detail: detail, Detail1: detail1, Detail2: detail2, Value: value, Properties: properties}, true
}

func (c *Client) BrowserGraph(ctx context.Context, processHint string, preferred model.ObjectID) (*model.Graph, error) {
	desktop := ObjectReference{Bus: BusName, Path: DesktopPath}
	children, err := c.children(ctx, desktop)
	if err != nil {
		return nil, fmt.Errorf("desktop children: %w", err)
	}
	var candidates []*model.Graph
	for _, child := range children {
		graph, buildErr := c.buildGraph(ctx, child)
		if buildErr != nil {
			c.logger.Debug("skip inaccessible application", "object", child.Model(), "error", buildErr)
			continue
		}
		root := graph.Nodes[graph.Root]
		if root == nil {
			continue
		}
		if !graphHasWebDocument(graph) {
			continue
		}
		candidates = append(candidates, graph)
	}
	if selected := selectBrowserGraph(candidates, processHint, preferred); selected != nil {
		return selected, nil
	}
	return nil, errors.New("no accessible applications registered")
}

func selectBrowserGraph(candidates []*model.Graph, processHint string, preferred model.ObjectID) *model.Graph {
	var selected *model.Graph
	best := -1
	processHint = strings.ToLower(strings.TrimSpace(processHint))
	for _, graph := range candidates {
		if graph == nil {
			continue
		}
		if preferred.Valid() {
			if _, ok := graph.Nodes[preferred]; ok {
				return graph
			}
		}
		rank := 0
		if root := graph.Nodes[graph.Root]; root != nil {
			name := strings.ToLower(root.Name + " " + root.Description + " " + root.Attributes["toolkit"])
			if processHint != "" && strings.Contains(name, processHint) {
				rank += 100
			}
			if root.HasState("active") {
				rank += 8
			}
		}
		for _, node := range graph.Nodes {
			if node.Role != "document web" && node.Role != "document frame" {
				continue
			}
			if node.HasState("showing") {
				rank += 2
			}
			if node.HasState("focused") {
				rank += 4
			}
		}
		// Prefer the newest equally ranked desktop child. Chromium can leave a
		// defunct bootstrap application registered while a navigated renderer is
		// already focused.
		if rank >= best {
			selected, best = graph, rank
		}
	}
	return selected
}

func graphHasWebDocument(graph *model.Graph) bool {
	for _, node := range graph.Nodes {
		role := strings.ToLower(node.Role)
		if role == "document web" || role == "document frame" {
			return true
		}
	}
	return false
}

func (c *Client) buildGraph(ctx context.Context, root ObjectReference) (*model.Graph, error) {
	const maxObjects = 100_000
	cache := c.cacheSnapshot(ctx, root)
	nodes := make(map[model.ObjectID]*model.Node)
	type item struct {
		ref    ObjectReference
		parent model.ObjectID
		depth  int
	}
	queue := []item{{ref: root}}
	for len(queue) > 0 {
		if len(nodes) >= maxObjects {
			return nil, errors.New("accessible graph exceeds object limit")
		}
		current := queue[0]
		queue = queue[1:]
		id := current.ref.Model()
		if _, exists := nodes[id]; exists {
			continue
		}
		if current.depth > 512 {
			return nil, errors.New("accessible graph exceeds depth limit")
		}
		var cached *CacheItem
		if cache != nil {
			if item, live := cache.items[id]; live {
				cached = &item
			}
		}
		node, err := c.readNodeWithRetry(ctx, current.ref, cached)
		if err != nil {
			if current.depth == 0 {
				return nil, err
			}
			continue
		}
		node.Parent = current.parent
		nodes[id] = node
		for _, childID := range node.Children {
			queue = append(queue, item{ref: ObjectReference{Bus: childID.Bus, Path: dbus.ObjectPath(childID.Path)}, parent: id, depth: current.depth + 1})
		}
	}
	pruneMissingChildren(nodes)
	graph, err := model.NewGraph(root.Model(), nodes, c.revision.Add(1))
	if err != nil {
		return nil, err
	}
	resolveGraphContext(graph)
	if err := validateActiveWebDocument(graph); err != nil {
		return nil, err
	}
	return graph, nil
}

type objectCacheSnapshot struct {
	items map[model.ObjectID]CacheItem
}

func (c *Client) cacheSnapshot(ctx context.Context, root ObjectReference) *objectCacheSnapshot {
	if !root.Valid() {
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var items []CacheItem
	if err := c.conn.Object(root.Bus, CachePath).CallWithContext(callCtx, InterfaceCache+".GetItems", 0).Store(&items); err != nil {
		c.logger.Debug("AT-SPI cache unavailable; using accessible traversal", "application", root.Model(), "error", err)
		return nil
	}
	snapshot := &objectCacheSnapshot{items: make(map[model.ObjectID]CacheItem, len(items))}
	for _, item := range items {
		if item.Object.Valid() {
			snapshot.items[item.Object.Model()] = item
		}
	}
	if _, containsRoot := snapshot.items[root.Model()]; !containsRoot {
		c.logger.Debug("AT-SPI cache omitted application root; using accessible traversal", "application", root.Model())
		return nil
	}
	return snapshot
}

func pruneMissingChildren(nodes map[model.ObjectID]*model.Node) {
	for _, node := range nodes {
		children := node.Children[:0]
		for _, child := range node.Children {
			if _, exists := nodes[child]; exists {
				children = append(children, child)
			}
		}
		node.Children = children
	}
}

func (c *Client) readNodeWithRetry(ctx context.Context, ref ObjectReference, cached *CacheItem) (*model.Node, error) {
	const attempts = 3
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		var node *model.Node
		attemptCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		node, err = c.readNodeUsingCache(attemptCtx, ref, cached)
		cancel()
		if err == nil {
			return node, nil
		}
		if errors.Is(err, context.DeadlineExceeded) {
			break
		}
		if attempt == attempts-1 {
			break
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, err
}

func validateActiveWebDocument(graph *model.Graph) error {
	if graph == nil {
		return errors.New("accessible graph is nil")
	}
	if err := validateActiveBrowserFrame(graph); err != nil {
		return err
	}
	var active *model.Node
	activeRank := -1
	for _, id := range graph.Order {
		node := graph.Nodes[id]
		if node == nil || (node.Role != "document web" && node.Role != "document frame") {
			continue
		}
		rank := 0
		if node.HasState("showing") {
			rank++
		}
		if node.HasState("focused") {
			rank += 2
		}
		if active == nil || rank >= activeRank {
			active, activeRank = node, rank
		}
	}
	if active == nil {
		return nil
	}
	if len(active.Children) == 0 {
		return fmt.Errorf("active web document %s has no accessible children", active.ID)
	}
	visited := map[model.ObjectID]bool{active.ID: true}
	queue := append([]model.ObjectID(nil), active.Children...)
	descendants := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true
		node := graph.Nodes[id]
		if node == nil {
			return fmt.Errorf("active web document %s references missing child %s", active.ID, id)
		}
		descendants++
		queue = append(queue, node.Children...)
	}
	if descendants == 0 {
		return fmt.Errorf("active web document %s has no accessible descendants", active.ID)
	}
	return nil
}

func validateActiveBrowserFrame(graph *model.Graph) error {
	var active *model.Node
	activeRank := -1
	for _, id := range graph.Order {
		node := graph.Nodes[id]
		if node == nil || node.Role != "frame" {
			continue
		}
		rank := 0
		if node.HasState("showing") {
			rank++
		}
		if node.HasState("active") {
			rank += 4
		}
		if active == nil || rank >= activeRank {
			active, activeRank = node, rank
		}
	}
	if active == nil {
		return nil
	}
	if len(active.Children) == 0 {
		return fmt.Errorf("active browser frame %s has no accessible children", active.ID)
	}
	visited := map[model.ObjectID]bool{active.ID: true}
	queue := append([]model.ObjectID(nil), active.Children...)
	hasWebDocument := false
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if visited[id] {
			continue
		}
		visited[id] = true
		node := graph.Nodes[id]
		if node == nil {
			return fmt.Errorf("active browser frame %s references missing child %s", active.ID, id)
		}
		if node.Role == "document web" || node.Role == "document frame" {
			hasWebDocument = true
		}
		queue = append(queue, node.Children...)
	}
	if !hasWebDocument {
		return fmt.Errorf("active browser frame %s has no web document", active.ID)
	}
	return nil
}

func (c *Client) ReadNode(ctx context.Context, id model.ObjectID) (*model.Node, error) {
	return c.readNode(ctx, ObjectReference{Bus: id.Bus, Path: dbus.ObjectPath(id.Path)})
}

func (c *Client) readNode(ctx context.Context, ref ObjectReference) (*model.Node, error) {
	return c.readNodeUsingCache(ctx, ref, nil)
}

func (c *Client) readNodeUsingCache(ctx context.Context, ref ObjectReference, cached *CacheItem) (*model.Node, error) {
	if !ref.Valid() {
		return nil, errors.New("invalid accessible reference")
	}
	object := c.conn.Object(ref.Bus, ref.Path)
	var name, description string
	var stateWords []uint32
	var interfaces []string
	var childIDs []model.ObjectID
	if cached != nil {
		name = cached.Name
		description = cached.Description
		stateWords = append([]uint32(nil), cached.States...)
		interfaces = append([]string(nil), cached.Interfaces...)
	} else {
		name, _ = stringProperty(ctx, object, InterfaceAccessible+".Name")
		description, _ = stringProperty(ctx, object, InterfaceAccessible+".Description")
	}
	locale, _ := stringProperty(ctx, object, InterfaceAccessible+".Locale")
	accessibleID, _ := stringProperty(ctx, object, InterfaceAccessible+".AccessibleId")
	var role string
	if err := object.CallWithContext(ctx, InterfaceAccessible+".GetRoleName", 0).Store(&role); err != nil {
		return nil, fmt.Errorf("role %s: %w", ref.Model(), err)
	}
	if cached == nil {
		_ = optionalCall(ctx, object, InterfaceAccessible+".GetState").Store(&stateWords)
	}
	attributes := make(map[string]string)
	if err := optionalCall(ctx, object, InterfaceAccessible+".GetAttributes").Store(&attributes); err != nil {
		attributes = nil
	}
	if cached == nil {
		_ = optionalCall(ctx, object, InterfaceAccessible+".GetInterfaces").Store(&interfaces)
	}
	children, _ := c.children(ctx, ref)
	childIDs = make([]model.ObjectID, 0, len(children))
	for _, child := range children {
		if child.Valid() {
			childIDs = append(childIDs, child.Model())
		}
	}
	node := &model.Node{ID: ref.Model(), Children: childIDs, Role: strings.ToLower(role), Name: name, Description: description, Locale: locale, AccessibleID: accessibleID, Interfaces: interfaces, States: DecodeStates(stateWords), Attributes: attributes}
	var relations []Relation
	if err := optionalCall(ctx, object, InterfaceAccessible+".GetRelationSet").Store(&relations); err == nil {
		node.Relations = make(map[string][]model.ObjectID, len(relations))
		for _, relation := range relations {
			name := relationName(relation.Type)
			for _, target := range relation.Targets {
				if target.Valid() {
					node.Relations[name] = append(node.Relations[name], target.Model())
				}
			}
		}
	}
	if slicesContains(interfaces, InterfaceText) {
		var text string
		if err := optionalCall(ctx, object, InterfaceText+".GetText", int32(0), int32(-1)).Store(&text); err == nil {
			node.Text = text
			if characterCount, countErr := int32Property(ctx, object, InterfaceText+".CharacterCount"); countErr == nil && characterCount > 0 {
				node.TextAttributeRuns = readTextAttributeRuns(ctx, object, characterCount)
			}
		}
		if caretOffset, caretErr := int32Property(ctx, object, InterfaceText+".CaretOffset"); caretErr == nil && caretOffset >= 0 {
			node.CaretOffset = int(caretOffset)
		}
		var selectionCount int32
		if err := optionalCall(ctx, object, InterfaceText+".GetNSelections").Store(&selectionCount); err == nil {
			for index := int32(0); index < selectionCount; index++ {
				var start, end int32
				// Text.GetSelection has two top-level output arguments, not one
				// D-Bus struct. Storing into a single Go struct silently rejected
				// every real Chromium selection.
				if err := optionalCall(ctx, object, InterfaceText+".GetSelection", index).Store(&start, &end); err != nil || end <= start {
					continue
				}
				node.Selections = append(node.Selections, model.TextRange{Object: node.ID, Start: int(start), End: int(end)})
			}
		}
	}
	if slicesContains(interfaces, InterfaceAction) {
		var actions []ActionDescription
		if err := optionalCall(ctx, object, InterfaceAction+".GetActions").Store(&actions); err == nil {
			for _, action := range actions {
				if value := strings.TrimSpace(action.KeyBinding); value != "" {
					node.KeyboardShortcut = value
					break
				}
			}
		}
	}
	if value := strings.TrimSpace(node.Attributes["keyshortcuts"]); value != "" {
		// Chromium's Action key binding can contain only the access-key
		// character ("x"), while the accessible keyshortcuts attribute retains
		// the modifier NVDA reports ("Alt+x"). Prefer the complete form.
		node.KeyboardShortcut = value
	}
	if slicesContains(interfaces, InterfaceHyperlink) {
		var uri string
		if err := optionalCall(ctx, object, InterfaceHyperlink+".GetURI", int32(0)).Store(&uri); err == nil {
			if uri = strings.TrimSpace(uri); uri != "" {
				if node.Attributes == nil {
					node.Attributes = make(map[string]string)
				}
				node.Attributes["url"] = uri
			}
		}
	}
	if slicesContains(interfaces, InterfaceComponent) {
		var extents Extents
		if err := optionalCall(ctx, object, InterfaceComponent+".GetExtents", uint32(0)).Store(&extents); err == nil {
			node.Bounds = model.Rect{X: int(extents.X), Y: int(extents.Y), Width: int(extents.Width), Height: int(extents.Height)}
		}
	}
	if slicesContains(interfaces, InterfaceTable) {
		if value, err := int32Property(ctx, object, InterfaceTable+".NRows"); err == nil {
			node.RowCount = int(value)
		}
		if value, err := int32Property(ctx, object, InterfaceTable+".NColumns"); err == nil {
			node.ColumnCount = int(value)
		}
	}
	if slicesContains(interfaces, InterfaceTableCell) {
		var position TableCellPosition
		if err := tupleProperty(ctx, object, InterfaceTableCell+".Position", &position); err == nil {
			// AT-SPI coordinates are zero based. Presentation and navigation use
			// human-facing one-based row and column numbers.
			node.Row, node.Column = int(position.Row)+1, int(position.Column)+1
		}
		if value, err := int32Property(ctx, object, InterfaceTableCell+".RowSpan"); err == nil && value > 0 {
			node.RowSpan = int(value)
		}
		if value, err := int32Property(ctx, object, InterfaceTableCell+".ColumnSpan"); err == nil && value > 0 {
			node.ColumnSpan = int(value)
		}
		var table ObjectReference
		if err := tupleProperty(ctx, object, InterfaceTableCell+".Table", &table); err == nil && table.Valid() {
			node.Table = table.Model()
		}
		var headers []ObjectReference
		if err := optionalCall(ctx, object, InterfaceTableCell+".GetRowHeaderCells").Store(&headers); err == nil {
			for _, header := range headers {
				if header.Valid() {
					node.RowHeaders = append(node.RowHeaders, header.Model())
				}
			}
		}
		headers = nil
		if err := optionalCall(ctx, object, InterfaceTableCell+".GetColumnHeaderCells").Store(&headers); err == nil {
			for _, header := range headers {
				if header.Valid() {
					node.ColumnHeaders = append(node.ColumnHeaders, header.Model())
				}
			}
		}
	}
	if slicesContains(interfaces, InterfaceValue) {
		if value, err := stringProperty(ctx, object, InterfaceValue+".Text"); err == nil {
			node.ValueText = value
		}
		if value, err := float64Property(ctx, object, InterfaceValue+".CurrentValue"); err == nil {
			node.CurrentValue = &value
		}
		if value, err := float64Property(ctx, object, InterfaceValue+".MinimumValue"); err == nil {
			node.MinimumValue = &value
		}
		if value, err := float64Property(ctx, object, InterfaceValue+".MaximumValue"); err == nil {
			node.MaximumValue = &value
		}
	}
	if value := node.Attributes["level"]; value != "" {
		node.HeadingLevel, _ = strconv.Atoi(value)
	}
	if node.HeadingLevel == 0 && node.Role == "heading" {
		tag := strings.ToLower(node.Attributes["tag"])
		if len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '9' {
			node.HeadingLevel = int(tag[1] - '0')
		}
	}
	if value := node.Attributes["posinset"]; value != "" {
		node.PositionInSet, _ = strconv.Atoi(value)
	}
	if value := node.Attributes["setsize"]; value != "" {
		node.SetSize, _ = strconv.Atoi(value)
	}
	return node, nil
}

func readTextAttributeRuns(ctx context.Context, object dbus.BusObject, characterCount int32) []model.TextAttributeRun {
	const maxRuns = 10_000
	runs := make([]model.TextAttributeRun, 0)
	for offset, count := int32(0), 0; offset < characterCount && count < maxRuns; count++ {
		attributes := make(map[string]string)
		var start, end int32
		err := optionalCall(ctx, object, InterfaceText+".GetAttributeRun", offset, false).Store(&attributes, &start, &end)
		if err != nil || end <= offset || start < 0 {
			break
		}
		if end > characterCount {
			end = characterCount
		}
		if len(attributes) > 0 {
			runs = append(runs, model.TextAttributeRun{Start: int(start), End: int(end), Attributes: attributes})
		}
		offset = end
	}
	return runs
}

func optionalCall(ctx context.Context, object dbus.BusObject, method string, arguments ...any) *dbus.Call {
	callCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	return object.CallWithContext(callCtx, method, 0, arguments...)
}

func resolveGraphContext(graph *model.Graph) {
	if graph == nil {
		return
	}
	resolve := func(ids []model.ObjectID) []string {
		values := make([]string, 0, len(ids))
		seen := map[string]bool{}
		for _, id := range ids {
			value := graphText(graph, id)
			if value != "" && !seen[value] {
				seen[value] = true
				values = append(values, value)
			}
		}
		return values
	}
	for _, node := range graph.Nodes {
		node.RowHeaderText = resolve(node.RowHeaders)
		node.ColumnHeaderText = resolve(node.ColumnHeaders)
		if len(node.Relations) == 0 {
			continue
		}
		node.RelationText = make(map[string][]string, len(node.Relations))
		for relation, ids := range node.Relations {
			if values := resolve(ids); len(values) > 0 {
				node.RelationText[relation] = values
			}
		}
	}
}

func graphText(graph *model.Graph, root model.ObjectID) string {
	if graph == nil || graph.Nodes[root] == nil {
		return ""
	}
	if value := graph.Nodes[root].SpokenContent(); value != "" {
		return value
	}
	values := make([]string, 0, 4)
	seenText := map[string]bool{}
	seenNodes := map[model.ObjectID]bool{root: true}
	queue := append([]model.ObjectID(nil), graph.Nodes[root].Children...)
	for index := 0; index < len(queue) && len(seenNodes) <= 4096; index++ {
		id := queue[index]
		if seenNodes[id] {
			continue
		}
		seenNodes[id] = true
		node := graph.Nodes[id]
		if node == nil {
			continue
		}
		if value := node.SpokenContent(); value != "" {
			if !seenText[value] {
				seenText[value] = true
				values = append(values, value)
			}
			continue
		}
		queue = append(queue, node.Children...)
	}
	return strings.Join(values, " ")
}

func (c *Client) children(ctx context.Context, ref ObjectReference) ([]ObjectReference, error) {
	var children []ObjectReference
	err := c.conn.Object(ref.Bus, ref.Path).CallWithContext(ctx, InterfaceAccessible+".GetChildren", 0).Store(&children)
	return children, err
}

func (c *Client) DoDefaultAction(ctx context.Context, id model.ObjectID) error {
	object := c.conn.Object(id.Bus, dbus.ObjectPath(id.Path))
	var actions []ActionDescription
	if err := object.CallWithContext(ctx, InterfaceAction+".GetActions", 0).Store(&actions); err != nil {
		return err
	}
	if len(actions) == 0 {
		return errors.New("accessible has no actions")
	}
	var done bool
	if err := object.CallWithContext(ctx, InterfaceAction+".DoAction", 0, int32(0)).Store(&done); err != nil {
		return err
	}
	if !done {
		return errors.New("accessible action returned false")
	}
	return nil
}

func (c *Client) GrabFocus(ctx context.Context, id model.ObjectID) error {
	object := c.conn.Object(id.Bus, dbus.ObjectPath(id.Path))
	var focused bool
	if err := object.CallWithContext(ctx, InterfaceComponent+".GrabFocus", 0).Store(&focused); err != nil {
		return err
	}
	if !focused {
		return errors.New("accessible focus request returned false")
	}
	return nil
}

func (c *Client) SetTextSelection(ctx context.Context, id model.ObjectID, start, end int) error {
	if start < 0 || end <= start {
		return fmt.Errorf("invalid text selection %d..%d", start, end)
	}
	object := c.conn.Object(id.Bus, dbus.ObjectPath(id.Path))
	var count int32
	if err := object.CallWithContext(ctx, InterfaceText+".GetNSelections", 0).Store(&count); err != nil {
		return err
	}
	var selected bool
	method := InterfaceText + ".AddSelection"
	arguments := []any{int32(start), int32(end)}
	if count > 0 {
		method = InterfaceText + ".SetSelection"
		arguments = []any{int32(0), int32(start), int32(end)}
	}
	if err := object.CallWithContext(ctx, method, 0, arguments...).Store(&selected); err != nil {
		return err
	}
	if !selected {
		return errors.New("accessible text selection returned false")
	}
	return nil
}

func (c *Client) GenerateMouseEvent(ctx context.Context, x, y int, name string) error {
	if x < 0 || y < 0 || !slicesContains([]string{"abs", "b1c", "b1p", "b1r", "b3c", "b3p", "b3r"}, name) {
		return fmt.Errorf("invalid mouse event %q at %d,%d", name, x, y)
	}
	controller := c.conn.Object(BusName, DeviceControllerPath)
	// GenerateMouseEvent returns no D-Bus values. Treat successful transport as
	// success; calling Store here produces dbus.Store: length mismatch.
	if err := controller.CallWithContext(ctx, InterfaceDeviceController+".GenerateMouseEvent", 0, int32(x), int32(y), name).Err; err != nil {
		return err
	}
	return nil
}

func stringProperty(ctx context.Context, object dbus.BusObject, name string) (string, error) {
	value, err := property(ctx, object, name)
	if err != nil {
		return "", err
	}
	result, ok := value.Value().(string)
	if !ok {
		return "", fmt.Errorf("property %s is not a string", name)
	}
	return result, nil
}

func int32Property(ctx context.Context, object dbus.BusObject, name string) (int32, error) {
	value, err := property(ctx, object, name)
	if err != nil {
		return 0, err
	}
	result, ok := value.Value().(int32)
	if !ok {
		return 0, fmt.Errorf("property %s is not an int32", name)
	}
	return result, nil
}

func float64Property(ctx context.Context, object dbus.BusObject, name string) (float64, error) {
	value, err := property(ctx, object, name)
	if err != nil {
		return 0, err
	}
	result, ok := value.Value().(float64)
	if !ok {
		return 0, fmt.Errorf("property %s is not a float64", name)
	}
	return result, nil
}

func tupleProperty(ctx context.Context, object dbus.BusObject, name string, destination any) error {
	value, err := property(ctx, object, name)
	if err != nil {
		return err
	}
	if err := dbus.Store([]any{value.Value()}, destination); err != nil {
		return fmt.Errorf("property %s has an invalid tuple: %w", name, err)
	}
	return nil
}

func property(ctx context.Context, object dbus.BusObject, name string) (dbus.Variant, error) {
	separator := strings.LastIndexByte(name, '.')
	if separator <= 0 || separator == len(name)-1 {
		return dbus.Variant{}, fmt.Errorf("invalid property name %q", name)
	}
	callCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	var value dbus.Variant
	err := object.CallWithContext(
		callCtx,
		"org.freedesktop.DBus.Properties.Get",
		0,
		name[:separator],
		name[separator+1:],
	).Store(&value)
	return value, err
}

func slicesContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (c *Client) WaitForBrowser(ctx context.Context, hint string, preferred model.ObjectID, interval time.Duration) (*model.Graph, error) {
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		graph, err := c.BrowserGraph(ctx, hint, preferred)
		if err == nil && graphHasWebDocument(graph) {
			// preferred ranks candidates; it is not a liveness requirement.
			// Chromium replaces accessible object IDs during navigation and DOM
			// reconstruction. Engine.Refresh remaps cursors after accepting the new
			// complete graph, so waiting for an obsolete ID can only time out.
			return graph, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
