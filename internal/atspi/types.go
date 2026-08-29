package atspi

import (
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/openhoo/hoovda/internal/model"
)

const (
	BusName                   = "org.a11y.atspi.Registry"
	RegistryPath              = dbus.ObjectPath("/org/a11y/atspi/registry")
	DesktopPath               = dbus.ObjectPath("/org/a11y/atspi/accessible/root")
	CachePath                 = dbus.ObjectPath("/org/a11y/atspi/cache")
	DeviceControllerPath      = dbus.ObjectPath("/org/a11y/atspi/registry/deviceeventcontroller")
	ListenerPath              = dbus.ObjectPath("/org/openhoo/hoovda/deviceeventlistener")
	InterfaceAccessible       = "org.a11y.atspi.Accessible"
	InterfaceCache            = "org.a11y.atspi.Cache"
	InterfaceAction           = "org.a11y.atspi.Action"
	InterfaceComponent        = "org.a11y.atspi.Component"
	InterfaceHyperlink        = "org.a11y.atspi.Hyperlink"
	InterfaceText             = "org.a11y.atspi.Text"
	InterfaceTable            = "org.a11y.atspi.Table"
	InterfaceTableCell        = "org.a11y.atspi.TableCell"
	InterfaceValue            = "org.a11y.atspi.Value"
	InterfaceRegistry         = "org.a11y.atspi.Registry"
	InterfaceDeviceController = "org.a11y.atspi.DeviceEventController"
	InterfaceDeviceListener   = "org.a11y.atspi.DeviceEventListener"
)

type InterfaceDescription struct {
	Name       string
	Methods    []string
	Signals    []string
	Properties map[string]string
}

type ObjectReference struct {
	Bus  string
	Path dbus.ObjectPath
}

func (r ObjectReference) Model() model.ObjectID {
	return model.ObjectID{Bus: r.Bus, Path: string(r.Path)}
}
func (r ObjectReference) Valid() bool {
	return r.Bus != "" && r.Path.IsValid() && r.Path != "/org/a11y/atspi/null"
}

type DeviceEvent struct {
	Type        uint32
	ID          int32
	HWCode      uint32
	Modifiers   uint32
	Timestamp   int32
	EventString string
	IsText      bool
}

type KeyDefinition struct {
	KeyCode   int32
	KeySym    int32
	KeyString string
	Unused    int32
}

type EventMode struct{ Synchronous, Preemptive, Global bool }

// Extents mirrors the D-Bus (iiii) tuple returned by Component.GetExtents.
// A named struct is required: storing the single tuple into four scalar
// destinations makes godbus reject an otherwise valid reply.
type Extents struct {
	X      int32
	Y      int32
	Width  int32
	Height int32
}

type TableCellPosition struct {
	Row    int32
	Column int32
}

type Relation struct {
	Type    uint32
	Targets []ObjectReference
}

// CacheItem mirrors the modern org.a11y.atspi.Cache.GetItems member signature.
// It is used as a liveness index only; readNode remains the authoritative source
// for the richer data required by the screen-reader model.
type CacheItem struct {
	Object      ObjectReference
	Application ObjectReference
	Parent      ObjectReference
	Index       int32
	ChildCount  int32
	Interfaces  []string
	Name        string
	Role        uint32
	Description string
	States      []uint32
}

// ActionDescription mirrors one (sss) member of Action.GetActions.
type ActionDescription struct {
	LocalizedName string
	Description   string
	KeyBinding    string
}

type NativeEvent struct {
	Name       string
	Source     model.ObjectID
	Detail     string
	Detail1    int32
	Detail2    int32
	Value      any
	Properties map[string]dbus.Variant
}

func ParseAttributes(values []string) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		key, item, ok := strings.Cut(value, ":")
		if ok && key != "" {
			result[key] = item
		}
	}
	return result
}

func DecodeStates(words []uint32) map[string]bool {
	result := map[string]bool{}
	for index, name := range stateNames {
		word := index / 32
		bit := uint(index % 32)
		if word < len(words) && words[word]&(uint32(1)<<bit) != 0 {
			result[name] = true
		}
	}
	return result
}

var stateNames = []string{
	"invalid", "active", "armed", "busy", "checked", "collapsed", "defunct", "editable",
	"enabled", "expandable", "expanded", "focusable", "focused", "has tooltip", "horizontal",
	"iconified", "modal", "multiline", "multiselectable", "opaque", "pressed", "resizable",
	"selectable", "selected", "sensitive", "showing", "single line", "stale", "transient",
	"vertical", "visible", "manages descendants", "indeterminate", "required", "truncated",
	"animated", "invalid", "supports autocompletion", "selectable text", "is default", "visited",
	"checkable", "has popup", "read only",
}

var relationNames = []string{
	"null", "label for", "labelled by", "controller for", "controlled by",
	"member of", "tooltip for", "node child of", "node parent of", "extended",
	"flows to", "flows from", "subwindow of", "embeds", "embedded by",
	"popup for", "parent window of", "description for", "described by", "details",
	"details for", "error message", "error for",
}

func relationName(value uint32) string {
	if int(value) >= len(relationNames) {
		return fmt.Sprintf("relation-%d", value)
	}
	return relationNames[value]
}

func DBusError(err error) *dbus.Error {
	if err == nil {
		return nil
	}
	return dbus.MakeFailedError(fmt.Errorf("hoovda: %w", err))
}
