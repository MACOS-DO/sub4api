package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"testing/iotest"

	"github.com/stretchr/testify/require"
)

func TestModelTraceChallengeMatchesBrowserReference(t *testing.T) {
	raw, err := os.ReadFile("testdata/modeltrace_challenges.json")
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name          string `json:"name"`
			RandomIndexes []byte `json:"random_indexes"`
			ExpectedCount int    `json:"expected_count"`
			Prompt        string `json:"prompt"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.Len(t, fixture.Cases, 5)
	for _, sample := range fixture.Cases {
		t.Run(sample.Name, func(t *testing.T) {
			challenge, err := newModelTraceChallenge(bytes.NewReader(sample.RandomIndexes))
			require.NoError(t, err)
			require.Equal(t, sample.ExpectedCount, challenge.ExpectedCount)
			require.Equal(t, sample.Prompt, challenge.Prompt)
		})
	}
}

func TestModelTraceChallengeAllCountsAndFreshDraws(t *testing.T) {
	var draws []byte
	for offset := byte(0); offset <= 40; offset++ {
		draws = append(draws, offset, 0, 0, 0, 0)
	}
	random := bytes.NewReader(draws)
	for count := 292; count <= 332; count++ {
		challenge, err := newModelTraceChallenge(random)
		require.NoError(t, err)
		require.Equal(t, count, challenge.ExpectedCount)
		require.Contains(t, challenge.Prompt, fmt.Sprintf(" %d 个 1 到 355（含端点）的整数。", count))
	}
	require.Zero(t, random.Len())
}

func TestModelTraceChallengeRandomFailureReturnsNoPartialChallenge(t *testing.T) {
	// Fail both the count draw and each of the four template draws.
	for available := 0; available < 5; available++ {
		t.Run(fmt.Sprint(available), func(t *testing.T) {
			challenge, err := newModelTraceChallenge(bytes.NewReader(make([]byte, available)))
			require.ErrorIs(t, err, io.EOF)
			require.Zero(t, challenge)
		})
	}
	entropyErr := errors.New("entropy unavailable")
	challenge, err := newModelTraceChallenge(iotest.ErrReader(entropyErr))
	require.ErrorIs(t, err, entropyErr)
	require.Zero(t, challenge)
}

func TestNewModelTraceChallenge(t *testing.T) {
	challenge, err := NewModelTraceChallenge()
	require.NoError(t, err)
	require.GreaterOrEqual(t, challenge.ExpectedCount, 292)
	require.LessOrEqual(t, challenge.ExpectedCount, 332)
	require.Contains(t, challenge.Prompt, fmt.Sprintf(" %d 个 1 到 355（含端点）的整数。", challenge.ExpectedCount))
}
