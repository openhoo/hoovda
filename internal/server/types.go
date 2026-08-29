package server

import (
	"time"

	"github.com/openhoo/hoovda/internal/events"
	"github.com/openhoo/hoovda/internal/model"
	"github.com/openhoo/hoovda/internal/profile"
	"github.com/openhoo/hoovda/internal/recording"
)

const ProtocolVersion = "2.0"

type Health struct {
	Status          string `json:"status"`
	ProtocolVersion string `json:"protocolVersion"`
	Version         string `json:"version"`
	Commit          string `json:"commit"`
	Profile         string `json:"profile"`
	Locale          string `json:"locale"`
	KeyboardLayout  string `json:"keyboardLayout"`
	Ready           bool   `json:"ready"`
}

type CreateSessionRequest struct {
	TestID    string `json:"testId"`
	Recording bool   `json:"recording"`
}

type Session struct {
	ID            string    `json:"id"`
	TestID        string    `json:"testId"`
	Recording     bool      `json:"recording"`
	StartSequence uint64    `json:"startSequence"`
	CreatedAt     time.Time `json:"createdAt"`
}

type ActionRequest struct {
	Command  string  `json:"command"`
	Argument *string `json:"argument,omitempty"`
}

type ActionResult struct {
	Command        string         `json:"command"`
	Gesture        string         `json:"gesture"`
	Delivery       string         `json:"delivery"`
	BeforeSequence uint64         `json:"beforeSequence"`
	Cursor         uint64         `json:"cursor"`
	TimedOut       bool           `json:"timedOut"`
	Events         []events.Event `json:"events"`
	State          StateResult    `json:"state"`
}

type RuntimeLocation struct {
	Left   int `json:"left"`
	Top    int `json:"top"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type RuntimeObject struct {
	ID                     string           `json:"id,omitempty"`
	Role                   *string          `json:"role"`
	Name                   *string          `json:"name"`
	Location               *RuntimeLocation `json:"location"`
	Visited                *bool            `json:"visited,omitempty"`
	Redacted               bool             `json:"redacted,omitempty"`
	QuickNavigationTargets []string         `json:"quickNavigationTargets,omitempty"`
}

type RuntimeMouse struct {
	X      int            `json:"x"`
	Y      int            `json:"y"`
	Object *RuntimeObject `json:"object"`
}

type StateResult struct {
	Ready                  bool             `json:"ready"`
	GraphRevision          uint64           `json:"graphRevision"`
	Cursor                 model.Cursor     `json:"cursor"`
	Browse                 *RuntimeObject   `json:"browse"`
	Navigator              *RuntimeObject   `json:"navigator"`
	Review                 *RuntimeObject   `json:"review"`
	ReviewCopyStart        *model.Cursor    `json:"reviewCopyStart,omitempty"`
	ReviewSelection        *model.TextRange `json:"reviewSelection,omitempty"`
	CursorInDocument       bool             `json:"cursorInDocument"`
	Focus                  *RuntimeObject   `json:"focus"`
	BrowserWindowActive    bool             `json:"browserWindowActive"`
	WebContentFocused      bool             `json:"webContentFocused"`
	SingleLetterNavigation bool             `json:"singleLetterNavigation"`
	NativeSelectionMode    bool             `json:"nativeSelectionMode"`
	Mouse                  *RuntimeMouse    `json:"mouse,omitempty"`
	LeftMouseLocked        bool             `json:"leftMouseLocked"`
	RightMouseLocked       bool             `json:"rightMouseLocked"`
	SpeechMode             string           `json:"speechMode"`
	SpeechPaused           bool             `json:"speechPaused"`
	BrailleOffset          int              `json:"brailleOffset"`
	BrailleTether          string           `json:"brailleTether"`
	LastSequence           uint64           `json:"lastSequence"`
}

type EventsResult struct {
	Cursor               uint64                        `json:"cursor"`
	TimedOut             bool                          `json:"timedOut"`
	Events               []events.Event                `json:"events"`
	PresentationSettings *profile.PresentationSettings `json:"presentationSettings,omitempty"`
}

type DocumentResult struct {
	Profile  string       `json:"profile"`
	Locale   string       `json:"locale"`
	Revision uint64       `json:"revision"`
	Nodes    []model.Node `json:"nodes"`
}

type FinishResult struct {
	SessionID string               `json:"sessionId"`
	Cursor    uint64               `json:"cursor"`
	Artifacts []recording.Artifact `json:"artifacts"`
}

type ActionsResult struct {
	Profile        string            `json:"profile"`
	KeyboardLayout string            `json:"keyboardLayout"`
	Commands       []profile.Command `json:"commands"`
}
