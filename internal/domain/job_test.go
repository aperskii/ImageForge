package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJobStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		status       JobStatus
		wantValid    bool
		wantTerminal bool
	}{
		{name: "pending", status: StatusPending, wantValid: true, wantTerminal: false},
		{name: "processing", status: StatusProcessing, wantValid: true, wantTerminal: false},
		{name: "done", status: StatusDone, wantValid: true, wantTerminal: true},
		{name: "failed", status: StatusFailed, wantValid: true, wantTerminal: true},
		{name: "unknown", status: JobStatus("canceled"), wantValid: false, wantTerminal: false},
		{name: "zero value", status: JobStatus(""), wantValid: false, wantTerminal: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.wantValid, tt.status.Valid())
			assert.Equal(t, tt.wantTerminal, tt.status.Terminal())
			assert.Equal(t, string(tt.status), tt.status.String())
		})
	}
}
