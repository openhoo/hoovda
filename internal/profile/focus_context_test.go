package profile

import (
	"testing"

	"github.com/openhoo/hoovda/internal/model"
)

func TestPresentFocusContextHonorsDescriptionSettingsWithoutDialogName(t *testing.T) {
	presenter, err := NewPresenter("en-US")
	if err != nil {
		t.Fatal(err)
	}
	settings := presenter.Settings()
	settings.ReportObjectDescriptions = false
	if err := presenter.SetSettings(settings); err != nil {
		t.Fatal(err)
	}
	dialog := &model.Node{Role: "dialog", Description: "Hidden description", Text: "Unrelated body", States: map[string]bool{"enabled": true}}
	button := &model.Node{Role: "push button", Name: "Close", States: map[string]bool{"enabled": true}}
	if got := presenter.PresentFocus(button, []*model.Node{dialog}); got.Speech != "dialog  Close  button" {
		t.Fatalf("unnamed dialog speech with descriptions disabled = %q", got.Speech)
	}
}

func TestPresentFocusSkipsNonPresentableAncestors(t *testing.T) {
	presenter, err := NewPresenter("en-US")
	if err != nil {
		t.Fatal(err)
	}
	button := &model.Node{Role: "push button", Name: "Continue", States: map[string]bool{"enabled": true}}
	for _, test := range []struct {
		name     string
		ancestor model.Node
	}{
		{"unnamed group text", model.Node{Role: "grouping", Text: "Descendant text"}},
		{"named layout panel", model.Node{Role: "panel", Name: "Layout"}},
		{"protected group", model.Node{Role: "grouping", Name: "Protected", Redacted: true}},
		{"hidden group", model.Node{Role: "grouping", Name: "Hidden", Attributes: map[string]string{"hidden": "true"}}},
		{"invisible group", model.Node{Role: "grouping", Name: "Invisible", States: map[string]bool{"enabled": true, "invisible": true}}},
		{"defunct dialog", model.Node{Role: "dialog", Name: "Defunct", States: map[string]bool{"enabled": true, "defunct": true}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := presenter.PresentFocus(button, []*model.Node{&test.ancestor}); got.Speech != "Continue  button" {
				t.Fatalf("unexpected ancestor context = %q", got.Speech)
			}
		})
	}
}

func TestPresentFocusContextHonorsPresentationSettings(t *testing.T) {
	group := &model.Node{
		Role: "grouping", Name: "Days", Description: "Choose a date",
		RelationText: map[string][]string{"described by": {"Month available"}}, States: map[string]bool{"enabled": true},
	}
	dialog := &model.Node{Role: "dialog", Name: "Confirmation", Description: "Review your choice", States: map[string]bool{"enabled": true}}
	button := &model.Node{Role: "push button", Name: "Close", States: map[string]bool{"enabled": true}}
	for _, test := range []struct {
		name      string
		configure func(*PresentationSettings)
		ancestor  *model.Node
		want      string
	}{
		{"default group", func(*PresentationSettings) {}, group, "Days  grouping  Choose a date  Month available  Close  button"},
		{"groupings off", func(s *PresentationSettings) { s.ReportGroupings = false }, group, "Close  button"},
		{"object descriptions off", func(s *PresentationSettings) { s.ReportObjectDescriptions = false }, group, "Days  grouping  Close  button"},
		{"aria description off", func(s *PresentationSettings) { s.ReportAriaDescription = false }, group, "Days  grouping  Month available  Close  button"},
		{"dialog with groupings off", func(s *PresentationSettings) { s.ReportGroupings = false }, dialog, "Confirmation  dialog  Review your choice  Close  button"},
		{"dialog descriptions off", func(s *PresentationSettings) { s.ReportObjectDescriptions = false }, dialog, "Confirmation  dialog  Close  button"},
	} {
		t.Run(test.name, func(t *testing.T) {
			presenter, err := NewPresenter("en-US")
			if err != nil {
				t.Fatal(err)
			}
			settings := presenter.Settings()
			test.configure(&settings)
			if err := presenter.SetSettings(settings); err != nil {
				t.Fatal(err)
			}
			if got := presenter.PresentFocus(button, []*model.Node{test.ancestor}); got.Speech != test.want {
				t.Fatalf("focus speech = %q, want %q", got.Speech, test.want)
			}
		})
	}
}

func TestPresentFocusLocalizesNamedGroupContext(t *testing.T) {
	presenter, err := NewPresenter("de-DE")
	if err != nil {
		t.Fatal(err)
	}
	group := &model.Node{Role: "panel", Name: "Tage", Attributes: map[string]string{"xml-roles": "group"}, States: map[string]bool{"enabled": true}}
	button := &model.Node{Role: "push button", Name: "Weiter", States: map[string]bool{"enabled": true}}
	if got := presenter.PresentFocus(button, []*model.Node{group}); got.Speech != "Tage  Gruppierung  Weiter  Schalter" {
		t.Fatalf("German group context = %q", got.Speech)
	}
}
