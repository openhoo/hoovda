package engine

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/openhoo/hoovda/internal/model"
)

func webDocument(n *model.Node) bool {
	return n != nil && (n.Role == "document web" || n.Role == "document frame")
}

// Chromium can keep focused objects in background windows and hidden tabs.
// Only a single active frame and an unambiguous outer page in it can displace
// a live browse cursor. Embedded documents belong to their containing page.
func foregroundDocument(g *model.Graph) *model.Node {
	var frame, only, shown *model.Node
	var pages, visible int
	for _, n := range g.Nodes {
		if n.Role == "frame" && n.HasState("active") {
			if frame != nil {
				return nil
			}
			frame = n
		}
	}
	if frame == nil {
		return nil
	}
	for _, n := range g.Nodes {
		if !webDocument(n) {
			continue
		}
		if root, _ := g.DocumentRoot(n.ID); root != n.ID || !isDescendantOf(g, n.ID, frame.ID) {
			continue
		}
		only, pages = n, pages+1
		if n.HasState("showing") {
			shown, visible = n, visible+1
		}
	}
	if visible == 1 {
		return shown
	}
	if pages == 1 {
		return only
	}
	return nil
}

// Refresh may finish before a queued native focus event. Recover the editor if
// the cursor is a document fallback or its old page has lost the foreground.
func (e *Engine) recoverDocumentFocusLocked(g *model.Graph) {
	if !e.webContentFocused && g.Nodes[e.focus] != nil {
		return
	}
	cursorDoc, ok := g.DocumentRoot(e.cursor.Object)
	if !ok {
		return
	}
	current := g.Nodes[cursorDoc]
	if !webDocument(g.Nodes[e.cursor.Object]) && current.HasState("showing") && documentPriority(g, current) >= 4 {
		// This browse position is already in a shown page of an active frame.
		// An ordinary same-page refresh only walks the cursor's ancestors.
		return
	}
	doc := foregroundDocument(g)
	if doc == nil || cursorDoc != doc.ID && !doc.HasState("showing") {
		return
	}
	if cursorDoc == doc.ID {
		// A browser focus event must not undo an explicit browse movement,
		// including documentStart while native focus is still on an editor.
		if !webDocument(g.Nodes[e.cursor.Object]) {
			return
		}
		if f := g.Nodes[e.focus]; f != nil && !webDocument(f) && g.InDocument(f.ID, doc.ID) {
			return
		}
	}
	var target *model.Node
	for _, n := range g.Nodes {
		if n.HasState("focused") && n.HasState("editable") && g.InDocument(n.ID, doc.ID) {
			if target != nil {
				return
			}
			target = n
		}
	}
	if target == nil {
		return
	}
	e.focus, e.navigator = target.ID, target.ID
	e.cursor = model.Cursor{Object: target.ID, Mode: "focus"}
	e.review = model.Cursor{Object: target.ID, Offset: max(0, target.CaretOffset), Mode: "object"}
	e.browserWindowActive, e.webContentFocused = true, true
}

func otherActiveFrame(g *model.Graph, frame model.ObjectID) bool {
	if g == nil || g.Nodes[g.Root] == nil {
		return false
	}
	for _, id := range g.Nodes[g.Root].Children {
		if n := g.Nodes[id]; n != nil && id != frame && n.Role == "frame" && n.HasState("active") {
			return true
		}
	}
	return false
}

func graphHasOtherActiveFrame(g *model.Graph, id model.ObjectID) bool {
	if g == nil {
		return false
	}
	for n, i := g.Nodes[id], 0; n != nil && i < 32; i++ {
		if n.Role == "frame" {
			return !n.HasState("active") && otherActiveFrame(g, n.ID)
		}
		n = g.Nodes[n.Parent]
	}
	return false
}

// foregroundWebPath reads a single live branch rather than all siblings on a
// loading page. A separate inactive window is a positive reason to ignore a
// queued focus gain; an incomplete or ambiguous branch falls back to Refresh.
func (e *Engine) foregroundWebPath(ctx context.Context, g *model.Graph, leaf *model.Node) ([]*model.Node, bool) {
	parents, ok := e.access.(interface {
		Parent(context.Context, model.ObjectID) (model.ObjectID, error)
	})
	if !ok || g == nil || leaf == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, 220*time.Millisecond)
	defer cancel()
	path := []*model.Node{leaf}
	seen := map[model.ObjectID]bool{leaf.ID: true}
	var document, frame *model.Node
	for len(path) <= 32 && ctx.Err() == nil {
		n := path[len(path)-1]
		if webDocument(n) {
			document = n
		}
		if n.Role == "frame" {
			frame = n
		}
		if n.Role == "application" && frame != nil && document != nil {
			if !frame.HasState("active") {
				return nil, otherActiveFrame(g, frame.ID)
			}
			slices.Reverse(path)
			model.RedactAncestorPath(path)
			return path, false
		}
		parent, err := parents.Parent(ctx, n.ID)
		if err != nil || !parent.Valid() || parent.Bus != leaf.ID.Bus || seen[parent] {
			break
		}
		next, err := e.access.ReadNode(ctx, parent)
		if err != nil || next == nil || next.ID != parent {
			break
		}
		seen[parent] = true
		// Direct AT-SPI reads do not include parent IDs. Attach it only to our
		// copy, so the application role and presentation context remain intact.
		copy := *n
		copy.Parent = parent
		path[len(path)-1] = &copy
		path = append(path, next)
	}
	return nil, false
}

// A live child may refer to a description or table header outside its parent
// chain. Resolve simple siblings under an already verified parent, including
// inherited redaction. Other references need the normal, complete graph.
func (e *Engine) resolveFocusPathPresentation(ctx context.Context, path []*model.Node) bool {
	if len(path) == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Millisecond)
	defer cancel()
	parents, ok := e.access.(interface {
		Parent(context.Context, model.ObjectID) (model.ObjectID, error)
	})
	if !ok {
		return false
	}
	reads := 0
	resolve := func(ids []model.ObjectID) ([]string, bool) {
		var values []string
		for _, id := range ids {
			if reads >= 8 || ctx.Err() != nil {
				return nil, false
			}
			reads++
			anchored := false
			for i := len(path) - 2; i >= 0; i-- {
				parent := path[i]
				if !slices.Contains(parent.Children, id) {
					continue
				}
				// A detached node can still appear in an ancestor's earlier
				// child list. Verify the live backlink before using its text.
				currentParent, err := parents.Parent(ctx, id)
				if err != nil || id.Bus != parent.ID.Bus || currentParent != parent.ID {
					return nil, false
				}
				node, err := e.access.ReadNode(ctx, id)
				if err != nil || node == nil || node.ID != id {
					return nil, false
				}
				branch := append(slices.Clone(path[:i+1]), node)
				model.RedactAncestorPath(branch)
				node = branch[len(branch)-1]
				value := node.SpokenContent()
				if value == "" && len(node.Children) > 0 {
					return nil, false
				}
				if value != "" && !slices.Contains(values, value) {
					values = append(values, value)
				}
				anchored = true
				break
			}
			if !anchored {
				return nil, false
			}
		}
		return values, true
	}
	for i, n := range path {
		copy := *n
		if copy.IsProtected() {
			continue
		}
		var ok bool
		if copy.RowHeaderText, ok = resolve(n.RowHeaders); !ok {
			return false
		}
		if copy.ColumnHeaderText, ok = resolve(n.ColumnHeaders); !ok {
			return false
		}
		copy.RelationText = cloneRelationText(n.RelationText)
		for _, rel := range []string{"described by", "details", "error message"} {
			if len(n.Relations[rel]) == 0 {
				continue
			}
			values, ok := resolve(n.Relations[rel])
			if !ok {
				return false
			}
			if copy.RelationText == nil {
				copy.RelationText = map[string][]string{}
			}
			copy.RelationText[rel] = values
		}
		path[i] = &copy
	}
	return ctx.Err() == nil
}

func inWebApplication(path []*model.Node) bool {
	for i := len(path) - 2; i >= 0; i-- {
		n := path[i]
		if webDocument(n) {
			break
		}
		if n.Role == "application" || slices.Contains(strings.Fields(n.Attributes["xml-roles"]), "application") {
			return true
		}
	}
	return false
}
