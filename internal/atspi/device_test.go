package atspi

import (
	"context"
	"testing"
)

func TestDeviceListenerHandlesPressNotRelease(t *testing.T) {
	called := 0
	listener := &DeviceListener{
		layout: "desktop",
		handler: func(_ context.Context, gesture string) (bool, error) {
			called++
			if gesture != "f6" {
				t.Fatalf("gesture = %q", gesture)
			}
			return false, nil
		},
	}
	if _, dbusErr := listener.NotifyEvent(DeviceEvent{Type: keyPressedEvent, EventString: "F6"}); dbusErr != nil {
		t.Fatal(dbusErr)
	}
	if _, dbusErr := listener.NotifyEvent(DeviceEvent{Type: keyReleasedEvent, EventString: "F6"}); dbusErr != nil {
		t.Fatal(dbusErr)
	}
	if called != 1 {
		t.Fatalf("handler calls = %d", called)
	}
}

func TestListenerGrabsAreSpecificAndCoverLayouts(t *testing.T) {
	for _, layout := range []string{"desktop", "laptop"} {
		grabs := listenerGrabs(layout)
		if len(grabs) == 0 {
			t.Fatalf("%s produced no grabs", layout)
		}
		for _, grab := range grabs {
			if len(grab.Keys) == 0 {
				t.Fatalf("%s mask %#x requests an unsafe AnyKey grab", layout, grab.Mask)
			}
		}
	}

	if !hasGrab(listenerGrabs("desktop"), 0, int32('h')) {
		t.Fatal("desktop next-heading grab missing")
	}
	if !hasGrab(listenerGrabs("desktop"), 1, int32('H')) {
		t.Fatal("desktop previous-heading grab missing")
	}
	if !hasGrab(listenerGrabs("desktop"), 0, 0xff63) {
		t.Fatal("desktop Insert virtual modifier grab missing")
	}
	if !hasGrab(listenerGrabs("laptop"), 0, 0xffe5) {
		t.Fatal("laptop Caps Lock virtual modifier grab missing")
	}
}

func TestShiftedNumberNormalizesToGestureKey(t *testing.T) {
	event := DeviceEvent{EventString: "!", Modifiers: 1}
	if got := normalizeEventKey(event); got != "1" {
		t.Fatalf("got %q", got)
	}
}

func hasGrab(grabs []listenerGrab, mask uint32, keysym int32) bool {
	for _, grab := range grabs {
		if grab.Mask != mask {
			continue
		}
		for _, key := range grab.Keys {
			if key.KeySym == keysym {
				return true
			}
		}
	}
	return false
}
