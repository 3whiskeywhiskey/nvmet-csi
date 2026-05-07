package rds

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyRouterOSCommand(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		wantFamily  string
		wantAction  string
		wantCommand CommandClass
	}{
		{
			name:        "disk print observes",
			command:     "/disk print detail where slot=pvc-test",
			wantFamily:  "disk",
			wantAction:  "print",
			wantCommand: CommandClassObserve,
		},
		{
			name:        "disk add mutates",
			command:     "/disk add type=file file-path=/storage-pool/test.img",
			wantFamily:  "disk",
			wantAction:  "add",
			wantCommand: CommandClassMutate,
		},
		{
			name:        "file remove mutates",
			command:     "/file remove [find name=\"storage-pool/test.img\"]",
			wantFamily:  "file",
			wantAction:  "remove",
			wantCommand: CommandClassMutate,
		},
		{
			name:        "monitor traffic observes",
			command:     "/disk monitor-traffic disk=pvc-test once",
			wantFamily:  "disk",
			wantAction:  "monitor_traffic",
			wantCommand: CommandClassObserve,
		},
		{
			name:        "unknown empty command",
			command:     "   ",
			wantFamily:  "unknown",
			wantAction:  "unknown",
			wantCommand: CommandClassUnknown,
		},
		{
			name:        "unknown action stays unknown",
			command:     "/disk weird action",
			wantFamily:  "disk",
			wantAction:  "weird",
			wantCommand: CommandClassUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyRouterOSCommand(tt.command)
			require.Equal(t, tt.wantFamily, got.Family)
			require.Equal(t, tt.wantAction, got.Action)
			require.Equal(t, tt.wantCommand, got.Class)
		})
	}
}
