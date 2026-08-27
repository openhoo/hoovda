package atspi

import (
	"strings"
	"testing"

	"github.com/openhoo/hoovda/internal/model"
)

func TestValidateActiveWebDocumentRejectsMissingChild(t *testing.T) {
	app := model.ObjectID{Bus: "app", Path: "/app"}
	staleDocument := model.ObjectID{Bus: "app", Path: "/stale"}
	staleHeading := model.ObjectID{Bus: "app", Path: "/stale/heading"}
	activeDocument := model.ObjectID{Bus: "app", Path: "/active"}
	missingChild := model.ObjectID{Bus: "app", Path: "/active/missing"}
	graph, err := model.NewGraph(app, map[model.ObjectID]*model.Node{
		app:            {ID: app, Role: "application", Children: []model.ObjectID{staleDocument, activeDocument}},
		staleDocument:  {ID: staleDocument, Parent: app, Role: "document web", Name: "Runtime", Children: []model.ObjectID{staleHeading}, States: map[string]bool{"focused": true, "showing": true}},
		staleHeading:   {ID: staleHeading, Parent: staleDocument, Role: "heading", Name: "Ready"},
		activeDocument: {ID: activeDocument, Parent: app, Role: "document web", Name: "Checkout", Children: []model.ObjectID{missingChild}, States: map[string]bool{"focused": true, "showing": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateActiveWebDocument(graph); err == nil || !strings.Contains(err.Error(), "references missing child") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestValidateActiveWebDocumentAcceptsCompleteNewestDocument(t *testing.T) {
	app := model.ObjectID{Bus: "app", Path: "/app"}
	staleFrame := model.ObjectID{Bus: "app", Path: "/stale-frame"}
	staleDocument := model.ObjectID{Bus: "app", Path: "/stale"}
	activeFrame := model.ObjectID{Bus: "app", Path: "/active-frame"}
	activeDocument := model.ObjectID{Bus: "app", Path: "/active"}
	activeHeading := model.ObjectID{Bus: "app", Path: "/active/heading"}
	graph, err := model.NewGraph(app, map[model.ObjectID]*model.Node{
		app:            {ID: app, Role: "application", Children: []model.ObjectID{staleFrame, activeFrame}},
		staleFrame:     {ID: staleFrame, Parent: app, Role: "frame", Name: "Runtime window", Children: []model.ObjectID{staleDocument}, States: map[string]bool{"showing": true}},
		staleDocument:  {ID: staleDocument, Parent: staleFrame, Role: "document web", Name: "Runtime", Children: []model.ObjectID{{Bus: "app", Path: "/stale/missing"}}, States: map[string]bool{"showing": true}},
		activeFrame:    {ID: activeFrame, Parent: app, Role: "frame", Name: "Checkout window", Children: []model.ObjectID{activeDocument}, States: map[string]bool{"active": true, "showing": true}},
		activeDocument: {ID: activeDocument, Parent: activeFrame, Role: "document web", Name: "Checkout", Children: []model.ObjectID{activeHeading}, States: map[string]bool{"focused": true, "showing": true}},
		activeHeading:  {ID: activeHeading, Parent: activeDocument, Role: "heading", Name: "Checkout"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateActiveWebDocument(graph); err != nil {
		t.Fatal(err)
	}
}

func TestValidateActiveWebDocumentRejectsMissingActiveFrameChild(t *testing.T) {
	app := model.ObjectID{Bus: "app", Path: "/app"}
	frame := model.ObjectID{Bus: "app", Path: "/frame"}
	missingDocument := model.ObjectID{Bus: "app", Path: "/document"}
	graph, err := model.NewGraph(app, map[model.ObjectID]*model.Node{
		app:   {ID: app, Role: "application", Children: []model.ObjectID{frame}},
		frame: {ID: frame, Parent: app, Role: "frame", Name: "Checkout window", Children: []model.ObjectID{missingDocument}, States: map[string]bool{"active": true, "showing": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateActiveWebDocument(graph); err == nil || !strings.Contains(err.Error(), "references missing child") {
		t.Fatalf("validation error = %v", err)
	}
}
