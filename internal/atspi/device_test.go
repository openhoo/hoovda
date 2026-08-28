package atspi

import (
	"context"
	"slices"
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
	if !hasGrab(listenerGrabs("laptop"), 0, int32('D')) || !hasGrab(listenerGrabs("laptop"), 2, int32('D')) {
		t.Fatal("laptop Caps Lock uppercase command-key grabs missing")
	}
}

func TestShiftedNumberNormalizesToGestureKey(t *testing.T) {
	event := DeviceEvent{EventString: "!", Modifiers: 1}
	if got := normalizeEventKey(event); got != "1" {
		t.Fatalf("got %q", got)
	}
}

func TestX11CapsLockNameNormalizesToVirtualModifier(t *testing.T) {
	if got := normalizeEventKey(DeviceEvent{EventString: "Caps_Lock"}); got != "capslock" {
		t.Fatalf("got %q", got)
	}
	var gestures []string
	listener := &DeviceListener{
		layout: "laptop",
		handler: func(_ context.Context, gesture string) (bool, error) {
			gestures = append(gestures, gesture)
			return true, nil
		},
	}
	for _, event := range []DeviceEvent{
		{Type: keyPressedEvent, EventString: "Caps_Lock"},
		{Type: keyPressedEvent, EventString: "d"},
		{Type: keyReleasedEvent, EventString: "Caps_Lock"},
	} {
		if _, dbusErr := listener.NotifyEvent(event); dbusErr != nil {
			t.Fatal(dbusErr)
		}
	}
	if !slices.Equal(gestures, []string{"capslock+d"}) {
		t.Fatalf("gestures = %#v", gestures)
	}
}

func TestPageKeysNormalizeFromX11NamesAndHardwareCodes(t *testing.T) {
	cases := []struct {
		event DeviceEvent
		want  string
	}{
		{event: DeviceEvent{EventString: "Prior"}, want: "pageup"},
		{event: DeviceEvent{EventString: "Next"}, want: "pagedown"},
		{event: DeviceEvent{EventString: "Page_Up"}, want: "pageup"},
		{event: DeviceEvent{EventString: "Page_Down"}, want: "pagedown"},
		{event: DeviceEvent{HWCode: 112}, want: "pageup"},
		{event: DeviceEvent{HWCode: 117}, want: "pagedown"},
	}
	for _, item := range cases {
		if got := normalizeEventKey(item.event); got != item.want {
			t.Fatalf("normalizeEventKey(%#v) = %q, want %q", item.event, got, item.want)
		}
	}
}

func TestVirtualModifierCannotStickAfterChord(t *testing.T) {
	var gestures []string
	listener := &DeviceListener{
		layout: "desktop",
		handler: func(_ context.Context, gesture string) (bool, error) {
			gestures = append(gestures, gesture)
			return true, nil
		},
	}
	events := []DeviceEvent{
		{Type: keyPressedEvent, EventString: "Insert"},
		{Type: keyPressedEvent, EventString: "d"},
		// Deliberately omit Insert release, matching the X11/AT-SPI edge case.
		{Type: keyPressedEvent, EventString: "Return"},
	}
	for _, event := range events {
		if _, dbusErr := listener.NotifyEvent(event); dbusErr != nil {
			t.Fatal(dbusErr)
		}
	}
	want := []string{"insert+d", "enter"}
	if !slices.Equal(gestures, want) {
		t.Fatalf("gestures = %#v, want %#v", gestures, want)
	}
}

func TestVirtualModifierSurvivesConstituentModifierPresses(t *testing.T) {
	var gestures []string
	listener := &DeviceListener{
		layout: "desktop",
		handler: func(_ context.Context, gesture string) (bool, error) {
			gestures = append(gestures, gesture)
			return true, nil
		},
	}
	events := []DeviceEvent{
		{Type: keyPressedEvent, EventString: "Insert"},
		{Type: keyPressedEvent, EventString: "Control_L", Modifiers: 4},
		{Type: keyPressedEvent, EventString: "f", Modifiers: 4},
	}
	for _, event := range events {
		if _, dbusErr := listener.NotifyEvent(event); dbusErr != nil {
			t.Fatal(dbusErr)
		}
	}
	want := []string{"ctrl+insert+f"}
	if !slices.Equal(gestures, want) {
		t.Fatalf("gestures = %#v, want %#v", gestures, want)
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
