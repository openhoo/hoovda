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

func TestRelationNameUsesPinnedATSPIEnumeration(t *testing.T) {
	if got := relationName(18); got != "described by" {
		t.Fatalf("relation 18 = %q", got)
	}
	if got := relationName(99); got != "relation-99" {
		t.Fatalf("unknown relation = %q", got)
	}
}

func TestResolveGraphContextDereferencesHeadersAndRelations(t *testing.T) {
	root := model.ObjectID{Bus: "app", Path: "/root"}
	header := model.ObjectID{Bus: "app", Path: "/header"}
	description := model.ObjectID{Bus: "app", Path: "/description"}
	details := model.ObjectID{Bus: "app", Path: "/details"}
	detailsText := model.ObjectID{Bus: "app", Path: "/details/text"}
	cell := model.ObjectID{Bus: "app", Path: "/cell"}
	graph, err := model.NewGraph(root, map[model.ObjectID]*model.Node{
		root:        {ID: root, Role: "document web", Children: []model.ObjectID{header, description, details, cell}},
		header:      {ID: header, Parent: root, Role: "column header", Name: "Price"},
		description: {ID: description, Parent: root, Role: "paragraph", Text: "Before tax"},
		details:     {ID: details, Parent: root, Role: "note", Children: []model.ObjectID{detailsText}},
		detailsText: {ID: detailsText, Parent: details, Role: "paragraph", Text: "Press to self-destruct"},
		cell: {
			ID:            cell,
			Parent:        root,
			Role:          "table cell",
			Name:          "12",
			ColumnHeaders: []model.ObjectID{header, header},
			Relations:     map[string][]model.ObjectID{"described by": {description}, "details": {details}},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	resolveGraphContext(graph)
	if got := graph.Nodes[cell].ColumnHeaderText; len(got) != 1 || got[0] != "Price" {
		t.Fatalf("headers = %#v", got)
	}
	if got := graph.Nodes[cell].RelationText["described by"]; len(got) != 1 || got[0] != "Before tax" {
		t.Fatalf("relation text = %#v", got)
	}
	if got := graph.Nodes[cell].RelationText["details"]; len(got) != 1 || got[0] != "Press to self-destruct" {
		t.Fatalf("nested relation text = %#v", got)
	}
}

func TestSelectBrowserGraphUsesFocusedObjectOverStaleExactMatch(t *testing.T) {
	staleRoot := model.ObjectID{Bus: "app", Path: "/stale-root"}
	staleDocument := model.ObjectID{Bus: "app", Path: "/stale-document"}
	activeRoot := model.ObjectID{Bus: "app", Path: "/active-root"}
	activeDocument := model.ObjectID{Bus: "app", Path: "/active-document"}
	stale, err := model.NewGraph(staleRoot, map[model.ObjectID]*model.Node{
		staleRoot:     {ID: staleRoot, Role: "application", Name: "Chromium", Children: []model.ObjectID{staleDocument}},
		staleDocument: {ID: staleDocument, Parent: staleRoot, Role: "document web", Name: "Bootstrap", States: map[string]bool{"focused": true, "showing": true}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	active, err := model.NewGraph(activeRoot, map[model.ObjectID]*model.Node{
		activeRoot:     {ID: activeRoot, Role: "application", Name: "Chromium", Children: []model.ObjectID{activeDocument}},
		activeDocument: {ID: activeDocument, Parent: activeRoot, Role: "document web", Name: "Checkout", States: map[string]bool{"focused": true, "showing": true}},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := selectBrowserGraph([]*model.Graph{stale, active}, "chromium", activeDocument); got != active {
		t.Fatalf("selected graph = %#v", got)
	}
	if got := selectBrowserGraph([]*model.Graph{stale, active}, "chromium", model.ObjectID{}); got != active {
		t.Fatalf("equal-rank fallback did not select newest child: %#v", got)
	}
}
