package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/openhoo/hoovda/internal/braille"
	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
	"github.com/openhoo/hoovda/internal/profile"
	"github.com/openhoo/hoovda/internal/synth"
)

type unavailableAnnouncementSource struct {
	*fakeAccess
	unavailable model.ObjectID
}

func (a *unavailableAnnouncementSource) ReadNode(ctx context.Context, id model.ObjectID) (*model.Node, error) {
	if id == a.unavailable {
		return nil, errors.New("native object became unavailable")
	}
	return a.fakeAccess.ReadNode(ctx, id)
}

func TestAnnouncementReadFailureDoesNotLeakProtectedContent(t *testing.T) {
	for _, test := range []struct {
		name      string
		cached    bool
		protected bool
		unknown   bool
		readable  bool
		allowText bool
	}{
		{name: "cached-protected", cached: true, protected: true},
		{name: "uncached-protected-backlink", protected: true},
		{name: "unknown-unreadable", unknown: true},
		{name: "cached-public", cached: true, allowText: true},
		{name: "readable-public", cached: true, readable: true, allowText: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := model.ObjectID{Bus: "app", Path: "/root"}
			parent := model.ObjectID{Bus: "app", Path: "/parent"}
			child := model.ObjectID{Bus: "app", Path: "/parent/child"}
			const text = "native-announcement-value"
			parentRole := "panel"
			if test.protected {
				parentRole = "password text"
			}
			nodes := map[model.ObjectID]*model.Node{
				root:   {ID: root, Role: "document web", Children: []model.ObjectID{parent}},
				parent: {ID: parent, Parent: root, Role: parentRole, Name: "Parent", Children: []model.ObjectID{child}},
			}
			if test.cached {
				nodes[child] = &model.Node{ID: child, Parent: parent, Role: "static", Name: text, Text: text}
			}
			if test.unknown {
				child = model.ObjectID{Bus: "app", Path: "/unknown"}
			}
			graph, err := model.NewGraph(root, nodes, 1)
			if err != nil {
				t.Fatal(err)
			}
			unavailable := child
			if test.readable {
				unavailable = model.ObjectID{}
			}
			access := &unavailableAnnouncementSource{fakeAccess: &fakeAccess{graph: graph, events: make(chan NativeEvent, 1)}, unavailable: unavailable}
			store := events.NewStore(100)
			presenter, err := profile.NewPresenter("en-US")
			if err != nil {
				t.Fatal(err)
			}
			value := New(access, store, presenter, braille.Passthrough{}, synth.Silence{SampleRate: 22_050}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Locale: "en-US", KeyboardLayout: "desktop"})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := value.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if err := value.BeginSession("protected-announcement"); err != nil {
				t.Fatal(err)
			}
			before := store.Cursor()
			value.handleNativeEvent(ctx, NativeEvent{Name: "org.a11y.atspi.Event.Object.Announcement", Source: child, Value: text})
			output, _, err := store.Snapshot(before, "protected-announcement")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, event := range output {
				if strings.Contains(event.Text, text) {
					found = true
					if !test.allowText {
						t.Fatalf("native read failure bypassed source protection: %#v", event)
					}
				}
			}
			if test.allowText && !found {
				t.Fatalf("public native announcement was discarded: %#v", output)
			}
		})
	}
}
