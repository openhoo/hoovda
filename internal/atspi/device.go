package atspi

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/openhoo/hoovda/internal/profile"
)

const (
	keyPressedEvent  = uint32(0)
	keyReleasedEvent = uint32(1)
	keyPressMask     = uint32(1 << keyPressedEvent)
	keyReleaseMask   = uint32(1 << keyReleasedEvent)
)

type GestureHandler func(context.Context, string) (bool, error)

type DeviceListener struct {
	mu           sync.Mutex
	heldModifier string
	layout       string
	handler      GestureHandler
}

func (c *Client) RegisterDeviceListener(ctx context.Context, layout string, handler GestureHandler) (*DeviceListener, error) {
	listener := &DeviceListener{layout: layout, handler: handler}
	if err := c.conn.Export(listener, ListenerPath, InterfaceDeviceListener); err != nil {
		return nil, fmt.Errorf("export device listener: %w", err)
	}
	controller := c.conn.Object(BusName, DeviceControllerPath)
	mode := EventMode{Synchronous: true, Preemptive: true, Global: true}
	for _, grab := range listenerGrabs(layout) {
		var registered bool
		if err := controller.CallWithContext(ctx, InterfaceDeviceController+".RegisterKeystrokeListener", 0, ListenerPath, grab.Keys, grab.Mask, keyPressMask|keyReleaseMask, mode).Store(&registered); err != nil {
			return nil, fmt.Errorf("register device listener for modifier mask %#x: %w", grab.Mask, err)
		}
		// at-spi2-core registers the listener and installs the global grabs
		// before replying, but its legacy registry implementation returns FALSE
		// unconditionally. The D-Bus transport error above is the only reliable
		// failure signal.
		_ = registered
	}
	return listener, nil
}

type listenerGrab struct {
	Mask uint32
	Keys []KeyDefinition
}

func listenerGrabs(layout string) []listenerGrab {
	const shiftLockMask = uint32(1 << 1)
	grouped := make(map[uint32]map[int32]KeyDefinition)
	add := func(mask uint32, key string, shifted bool) {
		definition, ok := keyDefinition(key, shifted)
		if !ok {
			return
		}
		if grouped[mask] == nil {
			grouped[mask] = make(map[int32]KeyDefinition)
		}
		grouped[mask][definition.KeySym] = definition
	}

	for _, command := range profile.Commands() {
		gestures := command.Desktop
		if layout == "laptop" {
			gestures = command.Laptop
		}
		for _, gesture := range gestures {
			parts := strings.Split(profile.NormalizeGesture(gesture), "+")
			if len(parts) == 0 {
				continue
			}
			var mask uint32
			virtualModifier := ""
			for _, modifier := range parts[:len(parts)-1] {
				switch modifier {
				case "shift":
					mask |= 1 << 0
				case "ctrl":
					mask |= 1 << 2
				case "alt":
					mask |= 1 << 3
				case "insert", "capslock":
					virtualModifier = modifier
				}
			}
			key := parts[len(parts)-1]
			add(mask, key, mask&1 != 0)
			if virtualModifier != "" {
				add(0, virtualModifier, false)
				if virtualModifier == "capslock" {
					// Some X servers apply LockMask before the grabbed press is
					// replayed. Register both states so Caps Lock always behaves as
					// HooVDA's virtual modifier, never as an ambient lock toggle.
					add(shiftLockMask, virtualModifier, false)
					add(mask|shiftLockMask, key, mask&1 != 0)
				}
			}
		}
	}

	masks := make([]uint32, 0, len(grouped))
	for mask := range grouped {
		masks = append(masks, mask)
	}
	slices.Sort(masks)
	result := make([]listenerGrab, 0, len(masks))
	for _, mask := range masks {
		keys := make([]KeyDefinition, 0, len(grouped[mask]))
		for _, definition := range grouped[mask] {
			keys = append(keys, definition)
		}
		slices.SortFunc(keys, func(a, b KeyDefinition) int { return int(a.KeySym - b.KeySym) })
		result = append(result, listenerGrab{Mask: mask, Keys: keys})
	}
	return result
}

func keyDefinition(key string, shifted bool) (KeyDefinition, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	if len([]rune(key)) == 1 {
		r := []rune(key)[0]
		if shifted {
			if r >= 'a' && r <= 'z' {
				r -= 'a' - 'A'
			} else if replacement, ok := map[rune]rune{'1': '!', '2': '@', '3': '#', '4': '$', '5': '%', '6': '^', '7': '&', '8': '*', '9': '(', '0': ')'}[r]; ok {
				r = replacement
			}
		}
		return KeyDefinition{KeySym: int32(r)}, true
	}
	keysyms := map[string]int32{
		"space": 0x20, "tab": 0xff09, "enter": 0xff0d, "escape": 0xff1b,
		"home": 0xff50, "left": 0xff51, "up": 0xff52, "right": 0xff53,
		"down": 0xff54, "pageup": 0xff55, "pagedown": 0xff56, "end": 0xff57,
		"insert": 0xff63, "capslock": 0xffe5,
	}
	if key == "tab" && shifted {
		return KeyDefinition{KeySym: 0xfe20}, true // ISO_Left_Tab
	}
	if value, ok := keysyms[key]; ok {
		return KeyDefinition{KeySym: value}, true
	}
	if strings.HasPrefix(key, "f") {
		number, err := strconv.Atoi(strings.TrimPrefix(key, "f"))
		if err == nil && number >= 1 && number <= 35 {
			return KeyDefinition{KeySym: int32(0xffbd + number)}, true
		}
	}
	return KeyDefinition{}, false
}

func (l *DeviceListener) NotifyEvent(event DeviceEvent) (bool, *dbus.Error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := normalizeEventKey(event)
	if key == "insert" || key == "capslock" {
		if event.Type == keyPressedEvent {
			l.heldModifier = key
		} else if event.Type == keyReleasedEvent && l.heldModifier == key {
			l.heldModifier = ""
		}
		return true, nil
	}
	if event.Type != keyPressedEvent {
		return false, nil
	}
	// XTest emits constituent modifier presses for multi-modifier chords such
	// as Insert+Ctrl+F. They are not the command key and must not consume the
	// one-shot virtual NVDA modifier.
	if isPhysicalModifierKey(key) {
		return false, nil
	}
	gesture := key
	modifiers := make([]string, 0, 4)
	if event.Modifiers&4 != 0 {
		modifiers = append(modifiers, "ctrl")
	}
	if event.Modifiers&8 != 0 {
		modifiers = append(modifiers, "alt")
	}
	if event.Modifiers&1 != 0 {
		modifiers = append(modifiers, "shift")
	}
	if l.heldModifier != "" {
		modifiers = append(modifiers, l.heldModifier)
		// Treat the NVDA modifier as one-shot for the command chord. Some X11
		// AT-SPI stacks omit the modifier release notification after a
		// preempted global gesture. Retaining it would turn the next unrelated
		// key into Insert+key or CapsLock+key and silently bypass its command.
		l.heldModifier = ""
	}
	if len(modifiers) > 0 {
		gesture = strings.Join(append(modifiers, key), "+")
	}
	gesture = profile.NormalizeGesture(gesture)
	if _, ok := profile.CommandByGesture(gesture, l.layout); !ok {
		return false, nil
	}
	consume, err := l.handler(context.Background(), gesture)
	return consume, DBusError(err)
}

func isPhysicalModifierKey(key string) bool {
	switch key {
	case "control", "control_l", "control_r", "ctrl", "shift", "shift_l", "shift_r", "alt", "alt_l", "alt_r", "meta_l", "meta_r", "super_l", "super_r":
		return true
	default:
		return false
	}
}

func normalizeEventKey(event DeviceEvent) string {
	value := strings.ToLower(strings.TrimSpace(event.EventString))
	if value != "" {
		if event.Modifiers&1 != 0 {
			if base, ok := map[string]string{"!": "1", "@": "2", "#": "3", "$": "4", "%": "5", "^": "6", "&": "7", "*": "8", "(": "9", ")": "0"}[value]; ok {
				return base
			}
		}
		switch value {
		case " ":
			return "space"
		case "\r", "\n":
			return "enter"
		case "prior", "page_up":
			return "pageup"
		case "next", "page_down":
			return "pagedown"
		}
		return value
	}
	keys := map[int32]string{
		9: "escape", 23: "tab", 36: "enter", 65: "space", 66: "capslock",
		110: "home", 111: "up", 112: "pageup", 113: "left", 114: "right",
		115: "end", 116: "down", 117: "pagedown", 118: "insert",
	}
	return keys[int32(event.HWCode)]
}
