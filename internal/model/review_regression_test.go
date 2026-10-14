package model

import (
	"fmt"
	"slices"
	"testing"
)

func TestWordRangesKeepCombiningMarksInTheirWord(t *testing.T) {
	text := "re\u0301sume\u0301 नमस्ते next"
	ranges, err := TextUnitRanges(ObjectID{Bus: "app", Path: "/text"}, text, TextUnitWord)
	if err != nil {
		t.Fatal(err)
	}
	var words []string
	for _, value := range ranges {
		word, err := value.Text(text)
		if err != nil {
			t.Fatal(err)
		}
		words = append(words, word)
	}
	if want := []string{"re\u0301sume\u0301 ", "नमस्ते ", "next"}; !slices.Equal(words, want) {
		t.Fatalf("word ranges = %#v, want %#v", words, want)
	}
}

func TestGraphRedactsContentBelowProtectedObjects(t *testing.T) {
	root := ObjectID{Bus: "app", Path: "/root"}
	password := ObjectID{Bus: "app", Path: "/password"}
	child := ObjectID{Bus: "app", Path: "/password/child"}
	raw := &Node{ID: child, Parent: password, Role: "static", Name: "raw-password-content", Text: "raw-password-content", Description: "raw-password-content", RowHeaderText: []string{"raw-password-row-header"}, ColumnHeaderText: []string{"raw-password-column-header"}}
	graph, err := NewGraph(root, map[ObjectID]*Node{
		root:     {ID: root, Role: "document web", Children: []ObjectID{password}},
		password: {ID: password, Parent: root, Role: "password text", Name: "Password", Children: []ObjectID{child}},
		child:    raw,
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range graph.Snapshot() {
		if node.ID == child && (!node.Redacted || node.Name != "[redacted]" || node.Text != "[redacted]" || node.Description != "" || len(node.RowHeaderText) != 0 || len(node.ColumnHeaderText) != 0) {
			t.Fatalf("snapshot exposes protected descendant: %#v", node)
		}
	}
	if got := graph.Nodes[child].SpokenContent(); got != "[redacted]" {
		t.Fatalf("protected descendant browse text = %q", got)
	}
	if raw.Name != "raw-password-content" || raw.Text != "raw-password-content" {
		t.Fatal("graph redaction mutated source node")
	}
}

func TestGraphKeepsSelectedOptionBelowHiddenMenu(t *testing.T) {
	for _, preRedacted := range []bool{false, true} {
		t.Run(fmt.Sprintf("preRedacted=%t", preRedacted), func(t *testing.T) {
			root := ObjectID{Bus: "app", Path: "/root"}
			menu := ObjectID{Bus: "app", Path: "/menu"}
			option := ObjectID{Bus: "app", Path: "/menu/option"}
			graph, err := NewGraph(root, map[ObjectID]*Node{
				root:   {ID: root, Role: "document web", Children: []ObjectID{menu}},
				menu:   {ID: menu, Parent: root, Role: "menu", Text: "hidden-menu-text", Attributes: map[string]string{"hidden": "true", "display": "none"}, Redacted: preRedacted, Children: []ObjectID{option}},
				option: {ID: option, Parent: menu, Role: "menu item", Name: "Germany", Text: "Germany", States: map[string]bool{"selected": true}},
			}, 1)
			if err != nil {
				t.Fatal(err)
			}
			if got := graph.Nodes[option]; got.IsProtected() || got.SpokenContent() != "Germany" {
				t.Fatalf("ordinary selected option was marked confidential: %#v", got)
			}
			for _, node := range graph.Snapshot() {
				if node.ID == menu && (!node.Redacted || node.Text != "[redacted]") {
					t.Fatalf("direct hidden-node text is not masked: %#v", node)
				}
				if node.ID == option && (node.Redacted || node.Name != "Germany" || node.Text != "Germany") {
					t.Fatalf("snapshot redacts ordinary selected option: %#v", node)
				}
			}
		})
	}
}

func TestGraphKeepsPasswordProtectionAcrossHiddenDescendant(t *testing.T) {
	root := ObjectID{Bus: "app", Path: "/root"}
	password := ObjectID{Bus: "app", Path: "/password"}
	hidden := ObjectID{Bus: "app", Path: "/password/hidden"}
	child := ObjectID{Bus: "app", Path: "/password/hidden/child"}
	graph, err := NewGraph(root, map[ObjectID]*Node{
		root:     {ID: root, Role: "document web", Children: []ObjectID{password}},
		password: {ID: password, Parent: root, Role: "password text", Attributes: map[string]string{"hidden": "true"}, Children: []ObjectID{hidden}},
		hidden:   {ID: hidden, Parent: password, Role: "panel", Attributes: map[string]string{"hidden": "true"}, Children: []ObjectID{child}},
		child:    {ID: child, Parent: hidden, Role: "static", Name: "password-content", Text: "password-content"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := graph.Nodes[child]; !got.IsProtected() || got.SpokenContent() != "[redacted]" {
		t.Fatalf("hidden descendant interrupted password protection: %#v", got)
	}
}
