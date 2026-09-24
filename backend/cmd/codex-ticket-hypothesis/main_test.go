package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCodexHypothesisReadsOnlyDedicatedEnv(t *testing.T) {
	t.Setenv("ACCESS_TOKEN", "machine-secret")
	path := filepath.Join(t.TempDir(), ".env")
	require.NoError(t, os.WriteFile(path, []byte("# isolated\nACCESS_TOKEN='file-token'\nMODEL=gpt-5.6-sol\n"), 0600))
	values, err := readEnvFile(path)
	require.NoError(t, err)
	require.Equal(t, "file-token", values["ACCESS_TOKEN"])
	require.Equal(t, "gpt-5.6-sol", values["MODEL"])
	require.Empty(t, values["CHATGPT_ACCOUNT_ID"])
}

func TestCodexHypothesisBundledModelTraceAssets(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	assets := map[string]string{
		"challenge-browser.mjs": "4a3868c9fe237cabde92f0d2bffaf32118f558f7a43fcc336874fecd2bf6be39",
		"fingerprint-core.mjs":  "83fa5bd611e18f8339122582335123c8ea168ed242298bb31f4e363abeeb6e4a",
		"unified_bank.json":     "1c2cb74d372f9f0f30d0dabbb7b7a838660d2f769a88d0c8489e4c662e088c21",
	}
	for name, expected := range assets {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(source), "modeltrace", name))
		require.NoError(t, err)
		sum := sha256.Sum256(data)
		require.Equal(t, expected, hex.EncodeToString(sum[:]))
	}
}

func TestCodexHypothesisModelTraceLocal(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node is required to execute the bundled ModelTrace algorithm")
	}
	challenges, err := generateChallenges(context.Background())
	require.NoError(t, err)
	require.Len(t, challenges, 1)
	lengths := make(map[int]bool)
	outputs := make([]service.CodexHypothesisOutput, 0, 1)
	for _, challenge := range challenges {
		require.NotEmpty(t, challenge.Prompt)
		require.GreaterOrEqual(t, challenge.ExpectedCount, 292)
		require.LessOrEqual(t, challenge.ExpectedCount, 332)
		require.False(t, lengths[challenge.ExpectedCount])
		lengths[challenge.ExpectedCount] = true
		values := make([]string, challenge.ExpectedCount)
		for index := range values {
			values[index] = fmt.Sprintf("%d", index%355+1)
		}
		outputs = append(outputs, service.CodexHypothesisOutput{Text: strings.Join(values, ", "), ExpectedCount: challenge.ExpectedCount})
	}
	result, err := scoreOutputs(context.Background(), outputs)
	require.NoError(t, err)
	require.Equal(t, 1, result.UsedOutputs)
	require.NotEmpty(t, result.Prediction)
	require.Greater(t, result.Probability, 0.0)
	require.LessOrEqual(t, result.Probability, 1.0)
	outputs[0].Text = "refusal"
	_, err = scoreOutputs(context.Background(), outputs)
	require.ErrorContains(t, err, "valid outputs 0/1")
}

func TestCodexHypothesisMissingNodeFailsBeforeNetwork(t *testing.T) {
	t.Setenv("PATH", "")
	_, err := generateChallenges(context.Background())
	require.ErrorContains(t, err, "Node is required")
}
