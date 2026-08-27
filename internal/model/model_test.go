package model

import "testing"

func TestGraphTraversalSkipsNonSemanticContainers(t *testing.T) {
	root := ObjectID{"app", "/root"}
	container := ObjectID{"app", "/container"}
	heading := ObjectID{"app", "/heading"}
	button := ObjectID{"app", "/button"}
	nodes := map[ObjectID]*Node{
		root:      {ID: root, Role: "document web", Children: []ObjectID{container}},
		container: {ID: container, Parent: root, Role: "panel", Children: []ObjectID{heading, button}},
		heading:   {ID: heading, Parent: container, Role: "heading", Name: "Title", HeadingLevel: 1},
		button:    {ID: button, Parent: container, Role: "push button", Name: "Save"},
	}
	graph, err := NewGraph(root, nodes, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Order) != 3 {
		t.Fatalf("semantic order length = %d", len(graph.Order))
	}
	next, ok := graph.Move(root, 1, func(node *Node) bool { return node.Role == "push button" })
	if !ok || next.ID != button {
		t.Fatalf("next button = %#v, %v", next, ok)
	}
}

func TestTextRangeUsesRuneOffsets(t *testing.T) {
	r, err := CharacterRange(ObjectID{"a", "/x"}, "A🙂B", 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Text("A🙂B")
	if err != nil || got != "🙂" {
		t.Fatalf("text = %q, err = %v", got, err)
	}
}

func TestSpokenContentDropsEmbeddedObjectReplacementCharacters(t *testing.T) {
	node := Node{Role: "landmark", Text: "\ufffc \ufffc\n\t"}
	if got := node.SpokenContent(); got != "" {
		t.Fatalf("spoken content = %q", got)
	}
}

func TestMoveInDocumentDoesNotEnterBrowserChromeOrAnotherTab(t *testing.T) {
	app := ObjectID{"app", "/app"}
	chromeButton := ObjectID{"app", "/chrome-button"}
	firstDocument := ObjectID{"app", "/document-1"}
	firstButton := ObjectID{"app", "/document-1/button"}
	secondDocument := ObjectID{"app", "/document-2"}
	secondButton := ObjectID{"app", "/document-2/button"}
	graph, err := NewGraph(app, map[ObjectID]*Node{
		app:            {ID: app, Role: "application", Name: "Chromium", Children: []ObjectID{chromeButton, firstDocument, secondDocument}},
		chromeButton:   {ID: chromeButton, Parent: app, Role: "push button", Name: "Minimize"},
		firstDocument:  {ID: firstDocument, Parent: app, Role: "document web", Name: "First", Children: []ObjectID{firstButton}},
		firstButton:    {ID: firstButton, Parent: firstDocument, Role: "push button", Name: "Continue"},
		secondDocument: {ID: secondDocument, Parent: app, Role: "document web", Name: "Second", Children: []ObjectID{secondButton}},
		secondButton:   {ID: secondButton, Parent: secondDocument, Role: "push button", Name: "Other"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	button, ok := graph.MoveInDocument(firstDocument, 1, func(node *Node) bool { return node.Role == "push button" })
	if !ok || button.ID != firstButton {
		t.Fatalf("button = %#v, %v", button, ok)
	}
	if _, ok := graph.MoveInDocument(firstButton, 1, func(node *Node) bool { return node.Role == "push button" }); ok {
		t.Fatal("navigation escaped current document")
	}
}
