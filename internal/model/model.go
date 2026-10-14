package model

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
)

type ObjectID struct {
	Bus  string `json:"bus"`
	Path string `json:"path"`
}

func (id ObjectID) String() string { return id.Bus + ":" + id.Path }

func (id ObjectID) Valid() bool {
	return id.Bus != "" && id.Path != "" && id.Path != "/org/a11y/atspi/null"
}

type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type TextAttributeRun struct {
	Start      int               `json:"start"`
	End        int               `json:"end"`
	Attributes map[string]string `json:"attributes"`
}

type Node struct {
	ID                ObjectID              `json:"id"`
	Parent            ObjectID              `json:"parent"`
	Children          []ObjectID            `json:"children"`
	Role              string                `json:"role"`
	Name              string                `json:"name"`
	Description       string                `json:"description,omitempty"`
	Text              string                `json:"text,omitempty"`
	TextAttributeRuns []TextAttributeRun    `json:"textAttributeRuns,omitempty"`
	Locale            string                `json:"locale,omitempty"`
	AccessibleID      string                `json:"accessibleId,omitempty"`
	KeyboardShortcut  string                `json:"keyboardShortcut,omitempty"`
	Interfaces        []string              `json:"interfaces,omitempty"`
	States            map[string]bool       `json:"states,omitempty"`
	Attributes        map[string]string     `json:"attributes,omitempty"`
	Relations         map[string][]ObjectID `json:"relations,omitempty"`
	Bounds            Rect                  `json:"bounds"`
	HeadingLevel      int                   `json:"headingLevel,omitempty"`
	Row               int                   `json:"row,omitempty"`
	Column            int                   `json:"column,omitempty"`
	RowSpan           int                   `json:"rowSpan,omitempty"`
	ColumnSpan        int                   `json:"columnSpan,omitempty"`
	RowCount          int                   `json:"rowCount,omitempty"`
	ColumnCount       int                   `json:"columnCount,omitempty"`
	Table             ObjectID              `json:"table,omitempty"`
	RowHeaders        []ObjectID            `json:"rowHeaders,omitempty"`
	ColumnHeaders     []ObjectID            `json:"columnHeaders,omitempty"`
	RowHeaderText     []string              `json:"rowHeaderText,omitempty"`
	ColumnHeaderText  []string              `json:"columnHeaderText,omitempty"`
	RelationText      map[string][]string   `json:"relationText,omitempty"`
	ValueText         string                `json:"valueText,omitempty"`
	CurrentValue      *float64              `json:"currentValue,omitempty"`
	MinimumValue      *float64              `json:"minimumValue,omitempty"`
	MaximumValue      *float64              `json:"maximumValue,omitempty"`
	PositionInSet     int                   `json:"positionInSet,omitempty"`
	SetSize           int                   `json:"setSize,omitempty"`
	CaretOffset       int                   `json:"caretOffset,omitempty"`
	Selections        []TextRange           `json:"selections,omitempty"`
	Redacted          bool                  `json:"redacted,omitempty"`
}

func (n Node) HasState(state string) bool { return n.States[state] }

func (n Node) HasInterface(name string) bool {
	return slices.Contains(n.Interfaces, name)
}

func (n Node) SpokenContent() string {
	if value := normalizeSpokenText(n.Name); value != "" {
		return value
	}
	if n.IsProtected() {
		return normalizeSpokenText(n.Description)
	}
	if value := normalizeSpokenText(n.Text); value != "" {
		return value
	}
	if value := normalizeSpokenText(n.ValueText); value != "" {
		return value
	}
	return normalizeSpokenText(n.Description)
}

func (n Node) IsProtected() bool {
	return n.Redacted || normalizeRole(n.Role) == "password text" || n.Attributes["hidden"] == "true"
}

func (n Node) protectsDescendantContent() bool {
	// Hidden controls mask their own text, but can contain ordinary accessible
	// children such as the selected option in a collapsed combo-box menu. A
	// driver may have already marked that hidden control redacted. Passwords
	// and explicit content redaction carry confidentiality into descendants.
	return normalizeRole(n.Role) == "password text" || (n.Redacted && n.Attributes["hidden"] != "true")
}

// RedactedContentCopy protects descendants whose accessible names and
// descriptions can contain the enclosing protected object's raw value.
func (n Node) RedactedContentCopy() Node {
	n.Redacted = true
	n.Name = "[redacted]"
	n.Description = ""
	n.RelationText = nil
	n.RowHeaderText, n.ColumnHeaderText = nil, nil
	n.CurrentValue, n.MinimumValue, n.MaximumValue = nil, nil, nil
	return n.RedactedCopy()
}

func (n Node) RedactedCopy() Node {
	copy := n
	if !copy.IsProtected() {
		return copy
	}
	copy.Redacted = true
	if copy.Text != "" {
		copy.Text = "[redacted]"
	}
	if copy.ValueText != "" {
		copy.ValueText = "[redacted]"
	}
	copy.TextAttributeRuns = nil
	copy.Selections = nil
	copy.CaretOffset = 0
	return copy
}

func normalizeSpokenText(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\ufffc' {
			return -1
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func (n Node) Semantic() bool {
	if n.HasState("invisible") || n.HasState("defunct") {
		return false
	}
	if n.SpokenContent() != "" {
		return true
	}
	switch normalizeRole(n.Role) {
	case "document web", "document frame", "heading", "link", "push button", "toggle button",
		"check box", "radio button", "combo box", "entry", "password text", "list", "list item",
		"table", "table cell", "row header", "column header", "landmark", "section", "article",
		"dialog", "alert", "status bar", "menu", "menu item", "page tab", "tree item", "slider",
		"progress bar", "image", "math", "separator", "blockquote", "embedded", "annotation":
		return true
	default:
		return false
	}
}

func normalizeRole(role string) string {
	return strings.ToLower(strings.TrimSpace(role))
}

type Graph struct {
	Root     ObjectID
	Nodes    map[ObjectID]*Node
	Order    []ObjectID
	Revision uint64
}

func NewGraph(root ObjectID, nodes map[ObjectID]*Node, revision uint64) (*Graph, error) {
	if !root.Valid() {
		return nil, fmt.Errorf("invalid graph root")
	}
	if _, ok := nodes[root]; !ok {
		return nil, fmt.Errorf("graph root is absent")
	}
	g := &Graph{Root: root, Nodes: maps.Clone(nodes), Revision: revision}
	visited := make(map[ObjectID]bool, len(nodes))
	var walk func(ObjectID, bool)
	walk = func(id ObjectID, protectedAncestor bool) {
		if visited[id] {
			return
		}
		visited[id] = true
		node, ok := nodes[id]
		if !ok {
			return
		}
		if protectedAncestor {
			redacted := node.RedactedContentCopy()
			node = &redacted
			g.Nodes[id] = node
		}
		if node.Semantic() {
			g.Order = append(g.Order, id)
		}
		for _, child := range node.Children {
			walk(child, protectedAncestor || node.protectsDescendantContent())
		}
	}
	walk(root, false)
	return g, nil
}

func (g *Graph) Node(id ObjectID) (*Node, bool) {
	if g == nil {
		return nil, false
	}
	node, ok := g.Nodes[id]
	return node, ok
}

// ProtectsNodeContent resolves confidentiality for fresh native reads, including
// children published before the graph contains their complete node. A cached
// parent chain or child backlink can still establish their protected scope.
func (g *Graph) ProtectsNodeContent(node *Node) bool {
	if g == nil || node == nil {
		return false
	}
	cached := g.Nodes[node.ID]
	if cached != nil && cached.Redacted {
		return true
	}
	protectedAncestor := func(id ObjectID) bool {
		for steps := 0; id.Valid() && steps < 512; steps++ {
			parent := g.Nodes[id]
			if parent == nil {
				return false
			}
			if parent.protectsDescendantContent() {
				return true
			}
			id = parent.Parent
		}
		return false
	}
	parent := node.Parent
	if !parent.Valid() && cached != nil {
		parent = cached.Parent
	}
	if protectedAncestor(parent) {
		return true
	}
	if cached == nil || !parent.Valid() {
		for _, candidate := range g.Nodes {
			if candidate != nil && slices.Contains(candidate.Children, node.ID) && protectedAncestor(candidate.ID) {
				return true
			}
		}
	}
	return false
}

func (g *Graph) Index(id ObjectID) int {
	if g == nil {
		return -1
	}
	return slices.Index(g.Order, id)
}

func (g *Graph) Move(current ObjectID, direction int, match func(*Node) bool) (*Node, bool) {
	if g == nil || len(g.Order) == 0 || direction == 0 {
		return nil, false
	}
	index := g.Index(current)
	if index < 0 {
		if direction > 0 {
			index = -1
		} else {
			index = len(g.Order)
		}
	}
	for next := index + direction; next >= 0 && next < len(g.Order); next += direction {
		node := g.Nodes[g.Order[next]]
		if node != nil && (match == nil || match(node)) {
			return node, true
		}
	}
	return nil, false
}

func (g *Graph) DocumentRoot(id ObjectID) (ObjectID, bool) {
	if g == nil {
		return ObjectID{}, false
	}
	var root ObjectID
	found := false
	for steps := 0; id.Valid() && steps < 512; steps++ {
		node := g.Nodes[id]
		if node == nil {
			break
		}
		role := normalizeRole(node.Role)
		if role == "document web" || role == "document frame" {
			root, found = id, true
		}
		id = node.Parent
	}
	return root, found
}

func (g *Graph) InDocument(id, document ObjectID) bool {
	root, ok := g.DocumentRoot(id)
	return ok && root == document
}

func (g *Graph) MoveInDocument(current ObjectID, direction int, match func(*Node) bool) (*Node, bool) {
	document, ok := g.DocumentRoot(current)
	if !ok {
		return nil, false
	}
	return g.Move(current, direction, func(node *Node) bool {
		return g.InDocument(node.ID, document) && (match == nil || match(node))
	})
}

func (g *Graph) Snapshot() []Node {
	if g == nil {
		return nil
	}
	result := make([]Node, 0, len(g.Order))
	for _, id := range g.Order {
		if node := g.Nodes[id]; node != nil {
			copy := node.RedactedCopy()
			result = append(result, copy)
		}
	}
	return result
}

func StableNodeLess(a, b Node) int {
	if value := cmp.Compare(a.ID.Bus, b.ID.Bus); value != 0 {
		return value
	}
	return cmp.Compare(a.ID.Path, b.ID.Path)
}

type Cursor struct {
	Object ObjectID `json:"object"`
	Offset int      `json:"offset"`
	Mode   string   `json:"mode"`
}

func (c Cursor) Validate(graph *Graph) error {
	if c.Mode != "browse" && c.Mode != "focus" {
		return fmt.Errorf("invalid cursor mode %q", c.Mode)
	}
	if _, ok := graph.Node(c.Object); !ok {
		return fmt.Errorf("cursor object is not in graph")
	}
	if c.Offset < 0 {
		return fmt.Errorf("cursor offset is negative")
	}
	return nil
}
