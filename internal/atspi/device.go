package atspi

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/openhoo/hoovda/internal/profile"
)

const (
	keyPressedEvent  = uint32(0)
	keyReleasedEvent = uint32(1)
	keyPressMask     = uint32(1 << keyPressedEvent)
	keyReleaseMask   = uint32(1 << keyReleasedEvent)
)

var keypadHardwareKeys = map[uint32]string{
	63: "numpadmultiply", 79: "numpad7", 80: "numpad8", 81: "numpad9",
	82: "numpadminus", 83: "numpad4", 84: "numpad5", 85: "numpad6",
	86: "numpadplus", 87: "numpad1", 88: "numpad2", 89: "numpad3",
	91: "numpaddelete", 104: "numpadenter", 106: "numpaddivide",
}

type GestureHandler func(context.Context, string) (bool, error)
type GestureConsumer func(string) bool
type deviceJob struct {
	gesture  string
	released <-chan struct{}
}
type pendingKey struct {
	released chan struct{}
	consume  bool
}

type DeviceListener struct {
	mu           sync.Mutex
	heldModifier string
	layout       string
	handler      GestureHandler
	consumer     GestureConsumer
	jobs         chan deviceJob
	pending      map[string]pendingKey
}

func (c *Client) RegisterDeviceListener(ctx context.Context, layout string, handler GestureHandler, consumer GestureConsumer) (*DeviceListener, error) {
	listener := &DeviceListener{layout: layout, handler: handler, consumer: consumer, jobs: make(chan deviceJob, 256), pending: make(map[string]pendingKey)}
	go listener.run(ctx)
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

func (l *DeviceListener) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-l.jobs:
			// Chromium can block accessibility-object replies until the grabbed key
			// finishes. Wait for the release callback before a graph-reading command
			// touches Chromium. The fallback covers stacks that omit grabbed releases.
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-job.released:
				timer.Stop()
			case <-timer.C:
			}
			_, _ = l.handler(context.Background(), job.gesture)
		}
	}
}

type listenerGrab struct {
	Mask uint32
	Keys []KeyDefinition
}

func listenerGrabs(layout string) []listenerGrab {
	const shiftLockMask = uint32(1 << 1)
	const numLockMask = uint32(1 << 14)
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
		// XKB resolves keypad digits to either their navigation or numeric
		// keysym depending on Num Lock and Shift. Register both variants: an
		// injected Shift+KP_Down can otherwise arrive as KP_2 and miss the
		// listener entirely before normalizeEventKey gets a chance to fold it.
		if alternate, ok := alternateKeypadDefinition(key); ok {
			grouped[mask][alternate.KeySym] = alternate
			// AT-SPI exposes Num Lock as modifier bit 14. Keypad events can
			// carry it even when XKB resolves the requested navigation keysym,
			// so install an equivalent grab for that ambient modifier state.
			if grouped[mask|numLockMask] == nil {
				grouped[mask|numLockMask] = make(map[int32]KeyDefinition)
			}
			grouped[mask|numLockMask][definition.KeySym] = definition
			grouped[mask|numLockMask][alternate.KeySym] = alternate
		}
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
			if mask&1 != 0 {
				// AT-SPI registries differ on whether a shifted number or
				// punctuation gesture is matched by its base or shifted keysym.
				// Register both under ShiftMask; the emitted event still normalizes
				// to one canonical command gesture.
				add(mask, key, false)
			}
			if virtualModifier != "" {
				add(0, virtualModifier, false)
				if virtualModifier == "capslock" {
					// Some X servers apply LockMask before the grabbed press is
					// replayed. Register both states so Caps Lock always behaves as
					// HooVDA's virtual modifier, never as an ambient lock toggle.
					add(shiftLockMask, virtualModifier, false)
					add(mask|shiftLockMask, key, mask&1 != 0)
					if mask&1 != 0 {
						// Caps Lock and Shift cancel each other for letters. The
						// resulting event therefore carries LockMask|ShiftMask but
						// the base lowercase keysym.
						add(mask|shiftLockMask, key, false)
					}
					// XKB can resolve a letter to its uppercase keysym while the
					// physical Caps Lock key remains down, even when the preemptive
					// AT-SPI listener consumed the Caps Lock press and therefore
					// reports no LockMask on the command key. Register both XKB
					// levels under both possible masks; normalizeEventKey folds the
					// delivered event string back to the command's lowercase key.
					add(mask, key, true)
					add(mask|shiftLockMask, key, true)
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

func alternateKeypadDefinition(key string) (KeyDefinition, bool) {
	keysyms := map[string]int32{
		"numpad1": 0xffb1, "numpad2": 0xffb2, "numpad3": 0xffb3,
		"numpad4": 0xffb4, "numpad5": 0xffb5, "numpad6": 0xffb6,
		"numpad7": 0xffb7, "numpad8": 0xffb8, "numpad9": 0xffb9,
		"numpadminus": '-', "numpadplus": '+', "numpadmultiply": '*',
		"numpaddivide": '/', "numpadenter": 0xff0d, "numpaddelete": 0xffff,
	}
	value, ok := keysyms[strings.ToLower(strings.TrimSpace(key))]
	return KeyDefinition{KeySym: value}, ok
}

func keyDefinition(key string, shifted bool) (KeyDefinition, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	if len([]rune(key)) == 1 {
		r := []rune(key)[0]
		if shifted {
			if r >= 'a' && r <= 'z' {
				r -= 'a' - 'A'
			} else if replacement, ok := map[rune]rune{
				'1': '!', '2': '@', '3': '#', '4': '$', '5': '%', '6': '^', '7': '&', '8': '*', '9': '(', '0': ')',
				',': '<', '.': '>', ';': ':', '\'': '"', '[': '{', ']': '}', '\\': '|', '-': '_', '=': '+', '`': '~',
			}[r]; ok {
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
		"numpad1": 0xff9c, "numpad2": 0xff99, "numpad3": 0xff9b, "numpad4": 0xff96,
		"numpad5": 0xff9d, "numpad6": 0xff98, "numpad8": 0xff97,
		"numpad7": 0xff95, "numpad9": 0xff9a, "numpadplus": 0xffab,
		"numpadminus": 0xffad, "numpaddivide": 0xffaf, "numpadmultiply": 0xffaa,
		"numpadenter":  0xff8d,
		"numpaddelete": 0xff9f, "backspace": 0xff08, "delete": 0xffff,
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
	if event.Type == keyReleasedEvent {
		if pending, ok := l.pending[deviceEventIdentity(event, key)]; ok {
			close(pending.released)
			delete(l.pending, deviceEventIdentity(event, key))
			return pending.consume, nil
		}
		if key == "insert" || key == "capslock" {
			if l.heldModifier == key {
				l.heldModifier = ""
			}
			return true, nil
		}
		return false, nil
	}
	if key == "insert" || key == "capslock" {
		l.heldModifier = key
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
	command, ok := profile.CommandByGesture(gesture, l.layout)
	if !ok {
		return false, nil
	}
	if l.jobs != nil && profile.CommandNeedsGraph(command) {
		consume := command.ConsumesBrowse
		if l.consumer != nil {
			consume = l.consumer(gesture)
		}
		releaseKey := deviceEventIdentity(event, key)
		if previous, ok := l.pending[releaseKey]; ok {
			close(previous.released)
		}
		released := make(chan struct{})
		l.pending[releaseKey] = pendingKey{released: released, consume: consume}
		select {
		case l.jobs <- deviceJob{gesture: gesture, released: released}:
			return consume, nil
		default:
			delete(l.pending, releaseKey)
			close(released)
			return false, DBusError(fmt.Errorf("device command queue is full"))
		}
	}
	consume, err := l.handler(context.Background(), gesture)
	return consume, DBusError(err)
}

func deviceEventIdentity(event DeviceEvent, key string) string {
	if event.HWCode != 0 {
		return fmt.Sprintf("hardware:%d", event.HWCode)
	}
	return "key:" + key
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
	// Keypad operators often expose ordinary text in EventString (for example
	// KP_Subtract reports "-"). Hardware codes are unambiguous for these
	// dedicated keys and must win before generic text normalization.
	if key, ok := keypadHardwareKeys[event.HWCode]; ok {
		return key
	}
	// XLookupString reports the main Delete key as the DEL control byte rather
	// than the symbolic name. Prefer its unambiguous keysym before interpreting
	// EventString so the gesture remains addressable.
	switch event.ID {
	case 0xffff:
		return "delete"
	case 0xff08:
		return "backspace"
	}
	if event.Modifiers&4 != 0 && event.ID >= 0x20 && event.ID <= 0x7e {
		// Control chords frequently replace EventString with an ASCII control
		// byte (for example Ctrl+[ becomes ESC). The X11 keysym still identifies
		// the physical printable key and is the stable gesture identity.
		value := strings.ToLower(string(rune(event.ID)))
		if base, ok := map[string]string{
			"!": "1", "@": "2", "#": "3", "$": "4", "%": "5", "^": "6", "&": "7", "*": "8", "(": "9", ")": "0",
			"<": ",", ">": ".", ":": ";", "\"": "'", "{": "[", "}": "]", "|": "\\", "_": "-", "+": "=", "~": "`",
		}[value]; ok {
			value = base
		}
		if value == " " {
			return "space"
		}
		return value
	}
	rawValue := strings.ToLower(event.EventString)
	if event.Modifiers&4 != 0 {
		runes := []rune(rawValue)
		if len(runes) == 1 && runes[0] >= 1 && runes[0] <= 26 {
			// XLookupString encodes Ctrl+A through Ctrl+Z as ASCII control
			// bytes. Preserve the underlying letter for gesture matching.
			return string(rune('a') + runes[0] - 1)
		}
	}
	value := strings.TrimSpace(rawValue)
	if value != "" {
		if event.Modifiers&1 != 0 {
			if base, ok := map[string]string{
				"!": "1", "@": "2", "#": "3", "$": "4", "%": "5", "^": "6", "&": "7", "*": "8", "(": "9", ")": "0",
				"<": ",", ">": ".", ":": ";", "\"": "'", "{": "[", "}": "]", "|": "\\", "_": "-", "+": "=", "~": "`",
				"exclam": "1", "at": "2", "numbersign": "3", "dollar": "4", "percent": "5", "asciicircum": "6",
				"ampersand": "7", "asterisk": "8", "parenleft": "9", "parenright": "0",
			}[value]; ok {
				return base
			}
		}
		switch value {
		case " ":
			return "space"
		case "\r", "\n":
			return "enter"
		case "caps_lock":
			return "capslock"
		case "prior", "page_up":
			return "pageup"
		case "next", "page_down":
			return "pagedown"
		case "kp_down", "kp_2":
			return "numpad2"
		case "kp_end", "kp_1":
			return "numpad1"
		case "kp_next", "kp_3":
			return "numpad3"
		case "kp_left", "kp_4":
			return "numpad4"
		case "kp_begin", "kp_5":
			return "numpad5"
		case "kp_right", "kp_6":
			return "numpad6"
		case "kp_up", "kp_8":
			return "numpad8"
		case "kp_home", "kp_7":
			return "numpad7"
		case "kp_prior", "kp_9":
			return "numpad9"
		case "kp_subtract":
			return "numpadminus"
		case "kp_add":
			return "numpadplus"
		case "kp_divide":
			return "numpaddivide"
		case "kp_multiply":
			return "numpadmultiply"
		case "kp_enter":
			return "numpadenter"
		case "kp_delete", "kp_decimal":
			return "numpaddelete"
		case "backspace":
			return "backspace"
		case "delete":
			return "delete"
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
