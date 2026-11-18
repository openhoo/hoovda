package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/openhoo/hoovda/internal/model"
	"github.com/openhoo/hoovda/internal/profile"
)

// Chromium can expose a rich edit as one embedded-object character per
// block. Its caret selects a child; the child's Text offsets are local to
// that block. Never combine these offsets with those of the parent.
func editableBrailleBlocks(g *model.Graph, editor *model.Node) []*model.Node {
	if len(editor.Children) == 0 || editor.Text != strings.Repeat("\ufffc", len(editor.Children)) {
		return nil
	}
	blocks := make([]*model.Node, 0, len(editor.Children))
	for _, id := range editor.Children {
		child := graphNode(g, id)
		if child == nil || child.Parent != editor.ID || child.Attributes["display"] != "block" {
			return nil // No unambiguous one-to-one mapping to visible lines.
		}
		blocks = append(blocks, child)
	}
	return blocks
}

func adjacentBrailleLine(nodes []*model.Node, index, offset, direction int) (*model.Node, model.TextRange, bool) {
	if index < 0 || index >= len(nodes) || nodes[index].IsProtected() {
		return nil, model.TextRange{}, false
	}
	node := nodes[index]
	lines, _ := model.TextUnitRanges(node.ID, navigableText(node), model.TextUnitLine)
	i := 0
	for i+1 < len(lines) && lines[i+1].Start <= offset {
		i++
	}
	if j := i + direction; j >= 0 && j < len(lines) {
		return node, lines[j], true
	}
	index += direction
	if index < 0 || index >= len(nodes) || nodes[index].IsProtected() {
		return nil, model.TextRange{}, false
	}
	node = nodes[index]
	lines, _ = model.TextUnitRanges(node.ID, navigableText(node), model.TextUnitLine)
	if len(lines) == 0 {
		return nil, model.TextRange{}, false
	}
	if direction < 0 {
		return node, lines[len(lines)-1], true
	}
	return node, lines[0], true
}

func (e *Engine) moveBrailleLine(ctx context.Context, command profile.Command) error {
	e.mu.RLock()
	graph, review, focus, navigator, tether := e.graph, e.review, e.focus, e.navigator, e.brailleTether
	e.mu.RUnlock()
	node := graphNode(graph, review.Object)
	focused := graphNode(graph, focus)
	if node == nil {
		node = graphNode(graph, navigator)
	}
	followCaret := tether != "review" && focused != nil && focused.HasState("editable")
	if followCaret || node == nil {
		node = focused
	}
	if node == nil {
		return errors.New("braille source object is unavailable")
	}
	if node.IsProtected() {
		return nil
	}

	var target *model.Node
	var value model.TextRange
	var fragment string
	var ok bool
	if node.HasState("editable") || strings.Contains(node.Text, "\n") {
		nodes, index, offset := []*model.Node{node}, 0, review.Offset
		if followCaret {
			live, err := e.access.ReadNode(ctx, node.ID)
			if err != nil {
				return err
			}
			if live == nil || live.ID != node.ID || live.IsProtected() {
				return nil
			}
			offset = live.CaretOffset
			if live.Text != node.Text {
				return errors.New("editable text changed during braille navigation")
			}
			if blocks := editableBrailleBlocks(graph, node); len(blocks) != 0 {
				if !slices.Equal(live.Children, node.Children) || live.Text != node.Text || offset < 0 || offset > len(blocks) {
					return errors.New("editable braille blocks changed during navigation")
				}
				nodes, index = blocks, min(offset, len(blocks)-1)
				offset = nodes[index].CaretOffset
			}
		} else if blocks := editableBrailleBlocks(graph, node); len(blocks) > 0 {
			// When review is still on the editor, its native caret identifies
			// the initial block. Later review commands use the child cursor.
			if node.CaretOffset < 0 || node.CaretOffset > len(blocks) {
				return nil
			}
			nodes, index = blocks, min(node.CaretOffset, len(blocks)-1)
			offset = nodes[index].CaretOffset
		} else if parent := graphNode(graph, node.Parent); parent != nil && parent.HasState("editable") && !parent.IsProtected() {
			if blocks := editableBrailleBlocks(graph, parent); len(blocks) > 0 {
				nodes, index = blocks, slices.Index(parent.Children, node.ID)
			}
		}
		target, value, ok = adjacentBrailleLine(nodes, index, offset, command.Direction)
		if !ok {
			return nil
		}
		var err error
		fragment, err = value.Text(navigableText(target))
		if err != nil {
			return err
		}
		if followCaret {
			if err := e.access.SetCaretOffset(ctx, target.ID, value.Start); err != nil {
				return fmt.Errorf("move editable braille caret: %w", err)
			}
		}
	} else {
		line, found := moveDocumentLine(graph, node.ID, command.Direction)
		if !found || len(line) == 0 {
			return nil
		}
		for _, part := range line {
			if part.IsProtected() {
				return nil
			}
		}
		target = line[0]
		fragment = e.presenter.PresentLine(line).Braille
	}
	e.mu.Lock()
	e.review.Object, e.review.Offset, e.brailleOffset = target.ID, value.Start, 0
	if tether != "review" {
		e.cursor.Object, e.cursor.Offset = target.ID, value.Start
	}
	e.mu.Unlock()
	e.emit(profile.Presentation{Braille: fragment}, target, command.ID, "brailleLine", "normal")
	return nil
}
