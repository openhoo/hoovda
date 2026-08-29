package input

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
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
		release := func(modifier string) error {
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			return run(releaseCtx, command, "keyup", modifier)
		}
		held := make([]string, 0, len(parts)-1)
		for _, modifier := range parts[:len(parts)-1] {
			if err := run(ctx, command, "keydown", modifier); err != nil {
				for index := len(held) - 1; index >= 0; index-- {
					_ = release(held[index])
				}
				return err
			}
			held = append(held, modifier)
		}
		keyErr := run(ctx, command, "key", parts[len(parts)-1])
		var releaseErr error
		for index := len(held) - 1; index >= 0; index-- {
			if err := release(held[index]); err != nil && releaseErr == nil {
				releaseErr = err
			}
		}
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
		case "numpad2":
			parts[index] = "KP_Down"
		case "numpad1":
			parts[index] = "KP_End"
		case "numpad3":
			parts[index] = "KP_Next"
		case "numpad4":
			parts[index] = "KP_Left"
		case "numpad5":
			parts[index] = "KP_Begin"
		case "numpad6":
			parts[index] = "KP_Right"
		case "numpad8":
			parts[index] = "KP_Up"
		case "numpad7":
			parts[index] = "KP_Home"
		case "numpad9":
			parts[index] = "KP_Prior"
		case "numpadminus":
			parts[index] = "KP_Subtract"
		case "numpadplus":
			parts[index] = "KP_Add"
		case "numpaddivide":
			parts[index] = "KP_Divide"
		case "numpadmultiply":
			parts[index] = "KP_Multiply"
		case "numpadenter":
			parts[index] = "KP_Enter"
		case "numpaddelete":
			parts[index] = "KP_Delete"
		case "backspace":
			parts[index] = "BackSpace"
		case "delete":
			parts[index] = "Delete"
		case ",":
			parts[index] = "comma"
		case ".":
			parts[index] = "period"
		case "/":
			parts[index] = "slash"
		case ";":
			parts[index] = "semicolon"
		case "'":
			parts[index] = "apostrophe"
		case "[":
			parts[index] = "bracketleft"
		case "]":
			parts[index] = "bracketright"
		case "\\":
			parts[index] = "backslash"
		case "-":
			parts[index] = "minus"
		case "=":
			parts[index] = "equal"
		case "`":
			parts[index] = "grave"
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
