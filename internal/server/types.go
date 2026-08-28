package server

import (
	"time"

	"github.com/openhoo/hoovda/internal/engine"
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
	State          engine.State   `json:"state"`
}

type EventsResult struct {
	Cursor   uint64         `json:"cursor"`
	TimedOut bool           `json:"timedOut"`
	Events   []events.Event `json:"events"`
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
