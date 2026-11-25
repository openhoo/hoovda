package atspi

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/openhoo/hoovda/internal/model"
)

// Read independent objects concurrently. Commit each level in native child
// order, so a second backlink or a late reply cannot change the chosen parent.
func walkNativeTree(ctx context.Context, root model.ObjectID, read func(model.ObjectID) (*model.Node, error)) (map[model.ObjectID]*model.Node, error) {
	const maxObjects = 100_000
	// Limit outstanding requests to Chromium on the shared accessibility bus.
	const maxReaders = 24
	type item struct{ id, parent model.ObjectID }
	level := []item{{id: root}}
	discovered := map[model.ObjectID]bool{root: true}
	nodes := make(map[model.ObjectID]*model.Node)
	for depth := 0; len(level) > 0; depth++ {
		if depth > 512 {
			return nil, errors.New("accessible graph exceeds depth limit")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		type result struct {
			node *model.Node
			err  error
		}
		results := make([]result, len(level))
		var next atomic.Int64
		var wg sync.WaitGroup
		worker := func() {
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= len(level) {
					break
				}
				results[i].node, results[i].err = read(level[i].id)
			}
		}
		workers := min(maxReaders, len(level))
		for range workers - 1 {
			wg.Go(worker)
		}
		worker()
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var children []item
		for i, r := range results {
			if r.err != nil {
				if depth == 0 || errors.Is(r.err, context.DeadlineExceeded) || errors.Is(r.err, context.Canceled) {
					return nil, r.err
				}
				continue
			}
			r.node.Parent = level[i].parent
			nodes[level[i].id] = r.node
			for _, child := range r.node.Children {
				if discovered[child] {
					continue
				}
				if len(discovered) >= maxObjects {
					return nil, errors.New("accessible graph exceeds object limit")
				}
				discovered[child] = true
				children = append(children, item{child, level[i].id})
			}
		}
		level = children
	}
	return nodes, nil
}
