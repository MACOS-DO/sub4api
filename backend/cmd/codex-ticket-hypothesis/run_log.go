package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type runLogger struct {
	mu       sync.Mutex
	terminal io.Writer
	file     io.Writer
	cancel   context.CancelFunc
	writeErr error
}

func (logger *runLogger) Write(data []byte) (int, error) {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	terminalCount, terminalErr := logger.terminal.Write(data)
	fileCount, fileErr := logger.file.Write(data)
	if terminalErr == nil && terminalCount != len(data) {
		terminalErr = io.ErrShortWrite
	}
	if fileErr == nil && fileCount != len(data) {
		fileErr = io.ErrShortWrite
	}
	if logger.writeErr == nil {
		switch {
		case fileErr != nil:
			logger.writeErr = fmt.Errorf("write run log: %w", fileErr)
		case terminalErr != nil:
			logger.writeErr = fmt.Errorf("write terminal: %w", terminalErr)
		}
		if logger.writeErr != nil {
			logger.cancel()
		}
	}
	if fileErr != nil {
		return fileCount, fileErr
	}
	if terminalErr != nil {
		return terminalCount, terminalErr
	}
	return len(data), nil
}

func (logger *runLogger) Err() error {
	logger.mu.Lock()
	defer logger.mu.Unlock()
	return logger.writeErr
}

func runLogged(envPath string, duration time.Duration, terminal io.Writer) error {
	return runLoggedWithReplay(envPath, "", duration, terminal)
}

func runLoggedWithReplay(envPath, sessionPath string, duration time.Duration, terminal io.Writer) error {
	started := time.Now()
	ctx, cancel := context.WithDeadline(context.Background(), started.Add(duration))
	defer cancel()
	file, path, err := createRunLog(envPath)
	if err != nil {
		failure := fmt.Errorf("create run log: %w", err)
		fmt.Fprintf(terminal, "无法判定: %v\n", failure)
		return failure
	}
	logger := &runLogger{terminal: terminal, file: file, cancel: cancel}
	fmt.Fprintf(logger, "日志文件：%s\n", path)
	runErr := run(ctx, started, envPath, sessionPath, duration, logger)
	if runErr != nil {
		fmt.Fprintf(logger, "无法判定: %v\n", runErr)
	}
	writeErr := logger.Err()
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		fmt.Fprintf(terminal, "日志写入失败: %v\n", writeErr)
	}
	if syncErr != nil {
		fmt.Fprintf(terminal, "日志同步失败: %v\n", syncErr)
	}
	if closeErr != nil {
		fmt.Fprintf(terminal, "日志关闭失败: %v\n", closeErr)
	}
	return errors.Join(runErr, writeErr, syncErr, closeErr)
}

func createRunLog(envPath string) (*os.File, string, error) {
	directory := filepath.Join(filepath.Dir(envPath), "logs")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, "", err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, "", err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, "", fmt.Errorf("log directory must be a private directory (0700): %s", directory)
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, "", err
	}
	name := time.Now().Format("20060102-150405.000000000") + "-" + hex.EncodeToString(suffix[:]) + ".log"
	path, err := filepath.Abs(filepath.Join(directory, name))
	if err != nil {
		return nil, "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, "", err
	}
	return file, path, nil
}
