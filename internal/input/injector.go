package input

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

type Injector interface {
	Press(context.Context, string) error
}

type XDoTool struct {
	Command string
	mu      sync.Mutex
}

func (x *XDoTool) Press(ctx context.Context, gesture string) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	command := x.Command
	if command == "" {
		command = "xdotool"
	}
	gesture = normalizeForXDoTool(gesture)
	parts := strings.Split(gesture, "+")
	if len(parts) > 1 && (parts[0] == "Insert" || parts[0] == "Caps_Lock") {
		modifier := parts[0]
		key := strings.Join(parts[1:], "+")
		if err := run(ctx, command, "keydown", modifier); err != nil {
			return err
		}
		keyErr := run(ctx, command, "key", key)
		releaseErr := run(ctx, command, "keyup", modifier)
		if keyErr != nil {
			return keyErr
		}
		return releaseErr
	}
	return run(ctx, command, "key", "--clearmodifiers", gesture)
}

func run(ctx context.Context, command string, arguments ...string) error {
	cmd := exec.CommandContext(ctx, command, arguments...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("inject key with %s: %w: %s", command, err, strings.TrimSpace(stderr.String()))
	}
	if message := strings.TrimSpace(stderr.String()); message != "" {
		return fmt.Errorf("inject key with %s reported an error: %s", command, message)
	}
	return nil
}

func normalizeForXDoTool(gesture string) string {
	parts := strings.Split(strings.ToLower(gesture), "+")
	for index, part := range parts {
		switch part {
		case "ctrl":
			parts[index] = "ctrl"
		case "alt":
			parts[index] = "alt"
		case "shift":
			parts[index] = "shift"
		case "insert":
			parts[index] = "Insert"
		case "capslock":
			parts[index] = "Caps_Lock"
		case "enter":
			parts[index] = "Return"
		case "escape":
			parts[index] = "Escape"
		case "space":
			parts[index] = "space"
		case "tab":
			parts[index] = "Tab"
		case "up":
			parts[index] = "Up"
		case "down":
			parts[index] = "Down"
		case "left":
			parts[index] = "Left"
		case "right":
			parts[index] = "Right"
		case "home":
			parts[index] = "Home"
		case "end":
			parts[index] = "End"
		case "pageup":
			parts[index] = "Prior"
		case "pagedown":
			parts[index] = "Next"
		default:
			if strings.HasPrefix(part, "f") {
				number, err := strconv.Atoi(strings.TrimPrefix(part, "f"))
				if err == nil && number >= 1 && number <= 35 {
					parts[index] = strings.ToUpper(part)
					continue
				}
			}
			parts[index] = part
		}
	}
	return strings.Join(parts, "+")
}
