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

func (c *Client) BrowserGraph(ctx context.Context, processHint string) (*model.Graph, error) {
	desktop := ObjectReference{Bus: BusName, Path: DesktopPath}
	children, err := c.children(ctx, desktop)
	if err != nil {
		return nil, fmt.Errorf("desktop children: %w", err)
	}
	var fallback *model.Graph
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
		name := strings.ToLower(root.Name + " " + root.Description + " " + root.Attributes["toolkit"])
		if strings.Contains(name, strings.ToLower(processHint)) {
			return graph, nil
		}
		if fallback == nil {
			fallback = graph
		}
	}
	if fallback != nil {
		return fallback, nil
	}
	return nil, errors.New("no accessible applications registered")
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
		node, err := c.readNodeWithRetry(ctx, current.ref)
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
	graph, err := model.NewGraph(root.Model(), nodes, c.revision.Add(1))
	if err != nil {
		return nil, err
	}
	if err := validateActiveWebDocument(graph); err != nil {
		return nil, err
	}
	return graph, nil
}

func (c *Client) readNodeWithRetry(ctx context.Context, ref ObjectReference) (*model.Node, error) {
	const attempts = 3
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		var node *model.Node
		node, err = c.readNode(ctx, ref)
		if err == nil {
			return node, nil
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
	if !ref.Valid() {
		return nil, errors.New("invalid accessible reference")
	}
	object := c.conn.Object(ref.Bus, ref.Path)
	name, _ := stringProperty(ctx, object, InterfaceAccessible+".Name")
	description, _ := stringProperty(ctx, object, InterfaceAccessible+".Description")
	locale, _ := stringProperty(ctx, object, InterfaceAccessible+".Locale")
	accessibleID, _ := stringProperty(ctx, object, InterfaceAccessible+".AccessibleId")
	var role string
	if err := object.CallWithContext(ctx, InterfaceAccessible+".GetRoleName", 0).Store(&role); err != nil {
		return nil, fmt.Errorf("role %s: %w", ref.Model(), err)
	}
	var stateWords []uint32
	_ = object.CallWithContext(ctx, InterfaceAccessible+".GetState", 0).Store(&stateWords)
	attributes := make(map[string]string)
	if err := object.CallWithContext(ctx, InterfaceAccessible+".GetAttributes", 0).Store(&attributes); err != nil {
		attributes = nil
	}
	var interfaces []string
	_ = object.CallWithContext(ctx, InterfaceAccessible+".GetInterfaces", 0).Store(&interfaces)
	children, _ := c.children(ctx, ref)
	childIDs := make([]model.ObjectID, 0, len(children))
	for _, child := range children {
		if child.Valid() {
			childIDs = append(childIDs, child.Model())
		}
	}
	node := &model.Node{ID: ref.Model(), Children: childIDs, Role: strings.ToLower(role), Name: name, Description: description, Locale: locale, AccessibleID: accessibleID, Interfaces: interfaces, States: DecodeStates(stateWords), Attributes: attributes}
	if slicesContains(interfaces, InterfaceText) {
		var text string
		if err := object.CallWithContext(ctx, InterfaceText+".GetText", 0, int32(0), int32(-1)).Store(&text); err == nil {
			node.Text = text
		}
	}
	if slicesContains(interfaces, InterfaceComponent) {
		var extents Extents
		if err := object.CallWithContext(ctx, InterfaceComponent+".GetExtents", 0, uint32(0)).Store(&extents); err == nil {
			node.Bounds = model.Rect{X: int(extents.X), Y: int(extents.Y), Width: int(extents.Width), Height: int(extents.Height)}
		}
	}
	if slicesContains(interfaces, InterfaceTable) {
		if value, err := int32Property(object, InterfaceTable+".NRows"); err == nil {
			node.RowCount = int(value)
		}
		if value, err := int32Property(object, InterfaceTable+".NColumns"); err == nil {
			node.ColumnCount = int(value)
		}
	}
	if slicesContains(interfaces, InterfaceTableCell) {
		var position TableCellPosition
		if err := tupleProperty(object, InterfaceTableCell+".Position", &position); err == nil {
			// AT-SPI coordinates are zero based. Presentation and navigation use
			// human-facing one-based row and column numbers.
			node.Row, node.Column = int(position.Row)+1, int(position.Column)+1
		}
	}
	if slicesContains(interfaces, InterfaceValue) {
		if value, err := stringProperty(ctx, object, InterfaceValue+".Text"); err == nil {
			node.ValueText = value
		}
		if value, err := float64Property(object, InterfaceValue+".CurrentValue"); err == nil {
			node.CurrentValue = &value
		}
		if value, err := float64Property(object, InterfaceValue+".MinimumValue"); err == nil {
			node.MinimumValue = &value
		}
		if value, err := float64Property(object, InterfaceValue+".MaximumValue"); err == nil {
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

func stringProperty(ctx context.Context, object dbus.BusObject, name string) (string, error) {
	value, err := object.GetProperty(name)
	if err != nil {
		return "", err
	}
	result, ok := value.Value().(string)
	if !ok {
		return "", fmt.Errorf("property %s is not a string", name)
	}
	return result, nil
}

func int32Property(object dbus.BusObject, name string) (int32, error) {
	value, err := object.GetProperty(name)
	if err != nil {
		return 0, err
	}
	result, ok := value.Value().(int32)
	if !ok {
		return 0, fmt.Errorf("property %s is not an int32", name)
	}
	return result, nil
}

func float64Property(object dbus.BusObject, name string) (float64, error) {
	value, err := object.GetProperty(name)
	if err != nil {
		return 0, err
	}
	result, ok := value.Value().(float64)
	if !ok {
		return 0, fmt.Errorf("property %s is not a float64", name)
	}
	return result, nil
}

func tupleProperty(object dbus.BusObject, name string, destination any) error {
	value, err := object.GetProperty(name)
	if err != nil {
		return err
	}
	if err := dbus.Store([]any{value.Value()}, destination); err != nil {
		return fmt.Errorf("property %s has an invalid tuple: %w", name, err)
	}
	return nil
}

func slicesContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (c *Client) WaitForBrowser(ctx context.Context, hint string, interval time.Duration) (*model.Graph, error) {
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		graph, err := c.BrowserGraph(ctx, hint)
		if err == nil && graphHasWebDocument(graph) {
			return graph, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
