package rds

import (
	"strings"
	"time"
)

// CommandClass identifies whether a RouterOS command observes or mutates state.
type CommandClass string

const (
	CommandClassObserve CommandClass = "observe"
	CommandClassMutate  CommandClass = "mutate"
	CommandClassUnknown CommandClass = "unknown"
)

// CommandClassification is the stable label set for RouterOS command telemetry.
type CommandClassification struct {
	Family string
	Action string
	Class  CommandClass
}

// CommandObserver records every RouterOS command sent by the SSH client.
type CommandObserver interface {
	RecordRDSCommand(family, action, class, status string, duration time.Duration)
}

// ClassifyRouterOSCommand extracts a conservative command family/action/class.
func ClassifyRouterOSCommand(command string) CommandClassification {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return CommandClassification{Family: "unknown", Action: "unknown", Class: CommandClassUnknown}
	}

	family := strings.TrimPrefix(fields[0], "/")
	if family == "" {
		family = "unknown"
	}
	action := "unknown"
	if len(fields) > 1 {
		action = strings.ToLower(fields[1])
	}

	return CommandClassification{
		Family: sanitizeCommandLabel(family),
		Action: sanitizeCommandLabel(action),
		Class:  classifyRouterOSAction(action),
	}
}

func classifyRouterOSAction(action string) CommandClass {
	switch strings.ToLower(action) {
	case "print", "monitor", "monitor-traffic", "find", "get", "export":
		return CommandClassObserve
	case "add", "remove", "set", "enable", "disable", "resize", "format", "reset", "move", "copy", "rename":
		return CommandClassMutate
	default:
		return CommandClassUnknown
	}
}

func sanitizeCommandLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "unknown"
	}
	value = strings.ReplaceAll(value, "/", "_")
	value = strings.ReplaceAll(value, "-", "_")
	return value
}
