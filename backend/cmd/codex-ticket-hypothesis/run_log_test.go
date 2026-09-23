package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexHypothesisRunLogCreatesPrivateUniqueFiles(t *testing.T) {
	directory := t.TempDir()
	envPath := filepath.Join(directory, ".env")
	for attempt := 0; attempt < 2; attempt++ {
		var terminal bytes.Buffer
		err := runLogged(envPath, time.Second, &terminal)
		require.ErrorContains(t, err, "open dedicated .env")
		files, err := os.ReadDir(filepath.Join(directory, "logs"))
		require.NoError(t, err)
		require.Len(t, files, attempt+1)
		var path string
		for _, file := range files {
			if strings.Contains(terminal.String(), file.Name()) {
				path = filepath.Join(directory, "logs", file.Name())
				break
			}
		}
		require.NotEmpty(t, path)
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, terminal.String(), string(content))
		require.Contains(t, string(content), "无法判定: open dedicated .env")
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
	info, err := os.Stat(filepath.Join(directory, "logs"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
}

func TestCodexHypothesisRunLogCreationFailure(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "logs"), []byte("not a directory"), 0600))
	var terminal bytes.Buffer
	err := runLogged(filepath.Join(directory, ".env"), time.Second, &terminal)
	require.ErrorContains(t, err, "create run log")
	require.Contains(t, terminal.String(), "无法判定: create run log")
	require.NotContains(t, terminal.String(), "open dedicated .env")
}

type failedLogFile struct{}

func (failedLogFile) Write([]byte) (int, error) {
	return 0, errors.New("disk full")
}

func TestCodexHypothesisRunLogWriteFailureCancelsRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var terminal bytes.Buffer
	logger := &runLogger{terminal: &terminal, file: failedLogFile{}, cancel: cancel}
	_, err := fmt.Fprint(logger, "x-codex-turn-state: secret\n")
	require.ErrorContains(t, err, "disk full")
	require.ErrorContains(t, logger.Err(), "write run log: disk full")
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Contains(t, terminal.String(), "x-codex-turn-state: secret")
}

func TestCodexHypothesisRunLogConcurrentBlocksMatchTerminal(t *testing.T) {
	var terminal, file bytes.Buffer
	logger := &runLogger{terminal: &terminal, file: &file, cancel: func() {}}
	var workers sync.WaitGroup
	for workerID := 1; workerID <= 3; workerID++ {
		workers.Add(1)
		go func(workerID int) {
			defer workers.Done()
			for index := 1; index <= 10; index++ {
				block := fmt.Sprintf("工位 %d #%d\nx-codex-turn-state: secret-%d\nCookie: session-%d\n", workerID, index, workerID, workerID)
				_, err := fmt.Fprint(logger, block)
				if err != nil {
					t.Errorf("write block: %v", err)
				}
			}
		}(workerID)
	}
	workers.Wait()
	require.NoError(t, logger.Err())
	require.Equal(t, terminal.String(), file.String())
	for workerID := 1; workerID <= 3; workerID++ {
		for index := 1; index <= 10; index++ {
			block := fmt.Sprintf("工位 %d #%d\nx-codex-turn-state: secret-%d\nCookie: session-%d\n", workerID, index, workerID, workerID)
			require.Equal(t, 1, strings.Count(file.String(), block))
		}
	}
}
