package atspi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openhoo/hoovda/internal/model"
)

func TestWalkNativeTreeRejectsPartialGraphOnCancel(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	child := model.ObjectID{Bus: "app", Path: "/child"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nodes, err := walkNativeTree(ctx, root, func(id model.ObjectID) (*model.Node, error) {
		if id == root {
			return &model.Node{ID: id, Children: []model.ObjectID{child}}, nil
		}
		cancel()
		return nil, ctx.Err()
	})
	if !errors.Is(err, context.Canceled) || nodes != nil {
		t.Fatalf("returned partial snapshot or lost cancellation: nodes=%v error=%v", nodes, err)
	}
}

func TestWalkNativeTreeKeepsSourceOrderAcrossConcurrentReads(t *testing.T) {
	id := func(s string) model.ObjectID { return model.ObjectID{Bus: "app", Path: "/" + s} }
	root, a, b, shared, missing := id("root"), id("a"), id("b"), id("shared"), id("defunct")
	children := map[model.ObjectID][]model.ObjectID{root: {a, b, missing}, a: {shared}, b: {shared}, shared: {root}}
	secondRead := make(chan struct{})
	nodes, err := walkNativeTree(context.Background(), root, func(i model.ObjectID) (*model.Node, error) {
		if i == b {
			close(secondRead)
		}
		if i == a {
			select {
			case <-secondRead:
			case <-time.After(time.Second):
				return nil, errors.New("independent siblings were serialized")
			}
		}
		c, ok := children[i]
		if !ok {
			return nil, errors.New("object disappeared")
		}
		return &model.Node{ID: i, Children: append([]model.ObjectID(nil), c...)}, nil
	})
	if err != nil || len(nodes) != 4 || nodes[shared].Parent != a || nodes[a].Parent != root || nodes[root].Parent.Valid() {
		t.Fatalf("unexpected traversal: %v %v", nodes, err)
	}
}

func TestSlowNativeObjectDoesNotDisappearFromGraph(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	child := model.ObjectID{Bus: "app", Path: "/slow"}
	nodes, err := walkNativeTree(context.Background(), root, func(id model.ObjectID) (*model.Node, error) {
		if id == child {
			return nil, context.DeadlineExceeded
		}
		return &model.Node{ID: id, Children: []model.ObjectID{child}}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || nodes != nil {
		t.Fatalf("slow object was pruned as if defunct: nodes=%v error=%v", nodes, err)
	}
}
