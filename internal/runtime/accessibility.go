package runtime

import (
	"context"
	"time"

	"github.com/openhoo/hoovda/internal/atspi"
	"github.com/openhoo/hoovda/internal/engine"
	"github.com/openhoo/hoovda/internal/model"
)

// Accessibility adapts the AT-SPI transport to the engine contract without
// coupling the engine to a D-Bus implementation.
type Accessibility struct {
	client *atspi.Client
	events chan engine.NativeEvent
}

func NewAccessibility(client *atspi.Client) *Accessibility {
	a := &Accessibility{client: client, events: make(chan engine.NativeEvent, 1024)}
	go a.forwardEvents()
	return a
}

func (a *Accessibility) BrowserGraph(ctx context.Context, hint string, preferred model.ObjectID) (*model.Graph, error) {
	return a.client.WaitForBrowser(ctx, hint, preferred, 25*time.Millisecond)
}

func (a *Accessibility) ReadNode(ctx context.Context, id model.ObjectID) (*model.Node, error) {
	return a.client.ReadNode(ctx, id)
}

func (a *Accessibility) DoDefaultAction(ctx context.Context, id model.ObjectID) error {
	return a.client.DoDefaultAction(ctx, id)
}

func (a *Accessibility) GrabFocus(ctx context.Context, id model.ObjectID) error {
	return a.client.GrabFocus(ctx, id)
}

func (a *Accessibility) SetTextSelection(ctx context.Context, id model.ObjectID, start, end int) error {
	return a.client.SetTextSelection(ctx, id, start, end)
}

func (a *Accessibility) GenerateMouseEvent(ctx context.Context, x, y int, name string) error {
	return a.client.GenerateMouseEvent(ctx, x, y, name)
}

func (a *Accessibility) Events() <-chan engine.NativeEvent { return a.events }

func (a *Accessibility) forwardEvents() {
	defer close(a.events)
	for event := range a.client.Events() {
		var valueObject model.ObjectID
		if reference, ok := event.Value.(atspi.ObjectReference); ok {
			valueObject = reference.Model()
		}
		a.events <- engine.NativeEvent{
			Name: event.Name, Source: event.Source, Detail: event.Detail,
			Detail1: event.Detail1, Detail2: event.Detail2, Value: event.Value, ValueObject: valueObject,
		}
	}
}
