package engine

import (
	"strings"

	"github.com/openhoo/hoovda/internal/model"
)

// Chromium changes a static browser label to the destination URL as keyboard
// focus moves between links. Its name does not change the page's hierarchy.
// Keep unknown objects, document objects and structural events on the full
// invalidation path. State exports still refresh on every dirty graph.
func chromeLeafNameChange(graph *model.Graph, event NativeEvent) bool {
	if !strings.HasSuffix(event.Name, ".PropertyChange") || event.Detail != "accessible-name" {
		return false
	}
	n, ok := graph.Node(event.Source)
	return ok && n.Role == "static" && len(n.Children) == 0 && !hasDocumentAncestor(graph, n.ID)
}

// A browser UI label might itself be focused, or might supply text to a
// relation on the focus path. For those cases retain the full refresh.
func webFocusIndependentOfChrome(graph *model.Graph, id model.ObjectID) bool {
	if !hasDocumentAncestor(graph, id) {
		return false
	}
	for depth := 0; depth <= 512; depth++ {
		n, ok := graph.Node(id)
		if !ok {
			return false
		}
		// Header and relation strings in the snapshot are used for presentation.
		for _, targets := range n.Relations {
			for _, target := range targets {
				if !hasDocumentAncestor(graph, target) {
					return false
				}
			}
		}
		for _, headers := range [][]model.ObjectID{n.RowHeaders, n.ColumnHeaders} {
			for _, target := range headers {
				if !hasDocumentAncestor(graph, target) {
					return false
				}
			}
		}
		if id == graph.Root {
			return true
		}
		id = n.Parent
	}
	return false
}
