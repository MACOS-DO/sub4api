package admin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexDiagnosticOutputRequiresCompletedResponse(t *testing.T) {
	for _, test := range []struct {
		name     string
		payload  string
		text     string
		complete bool
	}{
		{"completed stream", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"1, 2\"}\n\ndata: {\"type\":\"response.completed\"}\n\n", "1, 2", true},
		{"partial stream", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"1, 2\"}\n\n", "1, 2", false},
		{"failed stream", "data: {\"type\":\"response.failed\"}\n\n", "", false},
		{"completed JSON", `{"status":"completed","output_text":"3, 4"}`, "3, 4", true},
		{"partial JSON", `{"status":"incomplete","output_text":"3, 4"}`, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			text, complete := codexDiagnosticOutput([]byte(test.payload))
			require.Equal(t, test.complete, complete)
			require.Equal(t, test.text, text)
		})
	}
}
