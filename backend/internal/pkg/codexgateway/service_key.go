package codexgateway

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

func readServiceKey(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("cannot read Gateway service key file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("cannot read Gateway service key file")
	}
	// Match Gateway's file loader: only a trailing line ending is ignored.
	key := strings.TrimRight(string(raw), "\r\n")
	if !utf8.ValidString(key) || len(key) < 16 || strings.ContainsFunc(key, unicode.IsControl) {
		return "", errors.New("invalid Gateway service key")
	}
	return key, nil
}

// EnsureServiceKeyFile publishes a complete key exactly once on a shared local
// filesystem. A hard link is atomic and cannot replace another instance's key.
// Existing files, including invalid ones, are never overwritten.
func EnsureServiceKeyFile(path string) error {
	return ensureServiceKeyFile(path, rand.Reader, os.Link)
}

func ensureServiceKeyFile(path string, entropy io.Reader, publish func(string, string) error) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("Gateway service key file is required")
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("Gateway automatic service key must be a regular file")
		}
		_, err = readServiceKey(path)
		return err
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect Gateway service key file")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return errors.New("cannot create Gateway service key directory")
	}
	var random [32]byte
	if _, err := io.ReadFull(entropy, random[:]); err != nil {
		return errors.New("cannot generate Gateway service key")
	}
	file, err := os.CreateTemp(dir, ".service-key-*")
	if err != nil {
		return errors.New("cannot create temporary Gateway service key file")
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := io.WriteString(file, hex.EncodeToString(random[:])+"\n"); err != nil {
		return errors.New("cannot write Gateway service key file")
	}
	if err := file.Sync(); err != nil {
		return errors.New("cannot persist Gateway service key file")
	}
	if err := file.Close(); err != nil {
		return errors.New("cannot close Gateway service key file")
	}
	if err := publish(file.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return errors.New("cannot publish Gateway service key file")
		}
		// A concurrent initializer won. Use its complete key, not our candidate.
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			return errors.New("Gateway automatic service key must be a regular file")
		}
		_, err = readServiceKey(path)
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return errors.New("cannot persist Gateway service key directory")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("cannot persist Gateway service key directory")
	}
	return nil
}
