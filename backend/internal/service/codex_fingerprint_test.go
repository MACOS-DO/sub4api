package service

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelTraceGPTReferenceParity(t *testing.T) {
	raw, err := os.ReadFile("testdata/modeltrace_gpt_reference.json")
	require.NoError(t, err)
	var samples []struct {
		Model         string `json:"model"`
		ExpectedCount int    `json:"expected_count"`
		ParsedCount   int    `json:"parsed_count"`
		Text          string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(raw, &samples))
	require.Len(t, samples, 8)
	for _, sample := range samples {
		t.Run(sample.Model, func(t *testing.T) {
			prediction, commit, err := ModelTracePredictCommitted(sample.Text, sample.ExpectedCount)
			require.NoError(t, err)
			require.Equal(t, sample.Model, prediction.Model)
			require.Equal(t, sample.ParsedCount, prediction.ParsedCount)
			require.Greater(t, prediction.Probability, 0.97)
			require.Equal(t, modelTraceFallbackCommit, commit)
		})
	}
}

func TestModelTraceRejectsTruncatedChallenge(t *testing.T) {
	prediction, _, err := ModelTracePredictCommitted("1 2 3 4 5", 332)
	require.ErrorContains(t, err, "insufficient_numbers")
	require.Equal(t, 5, prediction.ParsedCount)
}

func TestModelTraceBankRejectsInvalidData(t *testing.T) {
	_, err := parseModelTraceBank([]byte(`{"schema":"robust-number-fingerprint-bank","models":[{"id":"gpt-1"}]}`))
	require.Error(t, err)
}
