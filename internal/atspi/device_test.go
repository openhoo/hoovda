package atspi

import (
	"context"
	"slices"
	"testing"
	"time"
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

func TestDeviceListenerDispatchesOutsideSynchronousDBusCallback(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	listener := &DeviceListener{
		layout:  "desktop",
		jobs:    make(chan deviceJob, 1),
		pending: make(map[string]pendingKey),
		consumer: func(gesture string) bool {
			return gesture == "insert+k"
		},
		handler: func(_ context.Context, gesture string) (bool, error) {
			if gesture != "insert+k" {
				t.Errorf("gesture = %q", gesture)
			}
			close(started)
			<-release
			return true, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go listener.run(ctx)
	listener.heldModifier = "insert"
	returned := make(chan bool, 1)
	go func() {
		consume, dbusErr := listener.NotifyEvent(DeviceEvent{Type: keyPressedEvent, ID: 'k', EventString: "k"})
		if dbusErr != nil {
			t.Errorf("NotifyEvent error: %v", dbusErr)
		}
		returned <- consume
	}()
	select {
	case consume := <-returned:
		if !consume {
			t.Fatal("recognized browse command was not consumed")
		}
	case <-time.After(time.Second):
		t.Fatal("NotifyEvent waited for command handler")
	}
	releaseConsumed, dbusErr := listener.NotifyEvent(DeviceEvent{Type: keyReleasedEvent, ID: 'k', EventString: "k"})
	if dbusErr != nil {
		t.Fatalf("release error: %v", dbusErr)
	}
	if !releaseConsumed {
		t.Fatal("consumed command press must also consume its release")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("queued command did not start")
	}
	close(release)
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
	if !hasGrab(listenerGrabs("desktop"), 1, int32('9')) || !hasGrab(listenerGrabs("desktop"), 1, int32('(')) {
		t.Fatal("desktop shifted-number base and shifted keysym grabs missing")
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
	for _, item := range []struct {
		value string
		want  string
	}{{"!", "1"}, {"exclam", "1"}, {"parenleft", "9"}} {
		if got := normalizeEventKey(DeviceEvent{EventString: item.value, Modifiers: 1}); got != item.want {
			t.Fatalf("%s normalized to %q, want %q", item.value, got, item.want)
		}
	}
}

func TestShiftedPunctuationNormalizesToGestureKey(t *testing.T) {
	if got := normalizeEventKey(DeviceEvent{EventString: "<", Modifiers: 1}); got != "," {
		t.Fatalf("got %q", got)
	}
	definition, ok := keyDefinition(",", true)
	if !ok || definition.KeySym != int32('<') {
		t.Fatalf("definition = %#v, ok = %v", definition, ok)
	}
}

func TestControlCharacterNormalizesToUnderlyingLetter(t *testing.T) {
	if got := normalizeEventKey(DeviceEvent{ID: 't', EventString: "\x14", Modifiers: 4}); got != "t" {
		t.Fatalf("Ctrl+T event normalized to %q", got)
	}
}

func TestControlPunctuationNormalizesFromKeysym(t *testing.T) {
	if got := normalizeEventKey(DeviceEvent{ID: '[', EventString: "\x1b", Modifiers: 4}); got != "[" {
		t.Fatalf("Ctrl+[ event normalized to %q", got)
	}
	if got := normalizeEventKey(DeviceEvent{ID: '{', EventString: "\x1b", Modifiers: 5}); got != "[" {
		t.Fatalf("Ctrl+Shift+[ event normalized to %q", got)
	}
}

func TestX11DeleteControlByteNormalizesFromKeysym(t *testing.T) {
	event := DeviceEvent{ID: 0xffff, HWCode: 119, EventString: "\x7f", Modifiers: 3}
	if got := normalizeEventKey(event); got != "delete" {
		t.Fatalf("Delete event normalized to %q", got)
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

func TestCapsLockShiftLetterGrabsBothXKBLevels(t *testing.T) {
	const shiftAndLock = uint32(1<<0 | 1<<1)
	grabs := listenerGrabs("laptop")
	if !hasGrab(grabs, shiftAndLock, int32('s')) || !hasGrab(grabs, shiftAndLock, int32('S')) {
		t.Fatal("CapsLock+Shift+S must grab both lowercase and uppercase keysyms")
	}
}

func TestNumpadTwoNormalizesForDesktopShortcutReport(t *testing.T) {
	for _, value := range []string{"KP_Down", "KP_2"} {
		if got := normalizeEventKey(DeviceEvent{EventString: value, Modifiers: 1}); got != "numpad2" {
			t.Fatalf("%s normalized to %q", value, got)
		}
	}
	if !hasGrab(listenerGrabs("desktop"), 1, 0xff99) {
		t.Fatal("desktop Shift+Numpad2 grab missing")
	}
	if !hasGrab(listenerGrabs("desktop"), 1, 0xffb2) {
		t.Fatal("desktop Shift+KP_2 alternate grab missing")
	}
	if !hasGrab(listenerGrabs("desktop"), 1|(1<<14), 0xff99) || !hasGrab(listenerGrabs("desktop"), 1|(1<<14), 0xffb2) {
		t.Fatal("desktop Shift+Numpad2 Num Lock grabs missing")
	}
	if got := normalizeEventKey(DeviceEvent{HWCode: 88, Modifiers: 1}); got != "numpad2" {
		t.Fatalf("empty-string X11 keypad event normalized to %q", got)
	}
}

func TestX11KeypadHardwareCodesNormalize(t *testing.T) {
	cases := map[uint32]string{
		63: "numpadmultiply", 79: "numpad7", 80: "numpad8", 81: "numpad9",
		82: "numpadminus", 83: "numpad4", 84: "numpad5", 85: "numpad6",
		86: "numpadplus", 87: "numpad1", 88: "numpad2", 89: "numpad3",
		91: "numpaddelete", 104: "numpadenter", 106: "numpaddivide",
	}
	for code, want := range cases {
		if got := normalizeEventKey(DeviceEvent{HWCode: code}); got != want {
			t.Fatalf("hardware code %d normalized to %q, want %q", code, got, want)
		}
	}
	if got := normalizeEventKey(DeviceEvent{HWCode: 82, EventString: "-"}); got != "numpadminus" {
		t.Fatalf("keypad operator text overrode hardware identity: %q", got)
	}
}

func TestXKBKeypadOperatorAlternatesAreGrabbed(t *testing.T) {
	want := map[int32]struct {
		key  string
		mask uint32
	}{
		'-': {"numpadminus", 0}, '+': {"numpadplus", 0}, '*': {"numpadmultiply", 0},
		'/': {"numpaddivide", 0}, 0xff0d: {"numpadenter", 0}, 0xffff: {"numpaddelete", 1},
	}
	grabs := listenerGrabs("desktop")
	for keysym, item := range want {
		if !hasGrab(grabs, item.mask, keysym) {
			t.Errorf("desktop %s alternate keysym %#x grab missing", item.key, keysym)
		}
		if !hasGrab(grabs, item.mask|(1<<14), keysym) {
			t.Errorf("desktop %s Num Lock alternate keysym %#x grab missing", item.key, keysym)
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
