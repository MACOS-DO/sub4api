package codexgateway

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func automaticKeyConfig(path string) Config {
	return Config{Enabled: true, BaseURL: "http://gateway:8787", ServiceKeyFile: path, AutoGenerateServiceKey: true}
}

func TestServiceKeyGeneratedOnceAndReused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persistent", "service_key")
	first, err := New(automaticKeyConfig(path))
	require.NoError(t, err)
	raw, err := hex.DecodeString(first.key)
	require.NoError(t, err)
	require.Len(t, raw, 32)
	file, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), file.Mode().Perm())
	directory, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), directory.Mode().Perm())
	second, err := New(automaticKeyConfig(path))
	require.NoError(t, err)
	require.True(t, first.key == second.key, "restart must reuse the persisted key")
	after, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, os.SameFile(file, after))
	require.Equal(t, file.ModTime(), after.ModTime())
	other, err := New(automaticKeyConfig(filepath.Join(t.TempDir(), "service_key")))
	require.NoError(t, err)
	require.True(t, first.key != other.key, "independent deployments need independent keys")
}

func TestServiceKeyConcurrentInitializersUseOneCompleteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service_key")
	const count = 32
	keys := make([]string, count)
	errors := make([]error, count)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			client, err := New(automaticKeyConfig(path))
			errors[i] = err
			if err == nil {
				keys[i] = client.key
			}
		}()
	}
	close(start)
	wg.Wait()
	for i := range count {
		require.NoError(t, errors[i])
		require.Len(t, keys[i], 64)
		require.True(t, keys[0] == keys[i], "all instances must read the published winner")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, entries, 1, "temporary candidates must be removed")
}

func TestServiceKeyInterruptedInitializationCannotExposePartialKey(t *testing.T) {
	for _, phase := range []string{"entropy", "publication"} {
		t.Run(phase, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "service_key")
			var entropy io.Reader = bytes.NewReader(make([]byte, 32))
			publish := os.Link
			if phase == "entropy" {
				entropy = bytes.NewReader(make([]byte, 10))
			} else {
				publish = func(temporary, destination string) error {
					_, err := os.Stat(destination)
					require.True(t, errors.Is(err, os.ErrNotExist))
					key, err := readServiceKey(temporary)
					require.NoError(t, err)
					require.Len(t, key, 64)
					return errors.New("private publication diagnostic")
				}
			}
			err := ensureServiceKeyFile(path, entropy, publish)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "private publication diagnostic")
			_, err = os.Stat(path)
			require.True(t, errors.Is(err, os.ErrNotExist))
			entries, err := os.ReadDir(filepath.Dir(path))
			require.NoError(t, err)
			require.Empty(t, entries)
			// A killed initializer may leave a private temporary candidate; it is
			// never treated as the final key by the next process.
			require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(path), ".service-key-orphan"), []byte("partial"), 0600))
			require.NoError(t, EnsureServiceKeyFile(path))
			key, err := readServiceKey(path)
			require.NoError(t, err)
			require.Len(t, key, 64)
		})
	}
}

func TestServiceKeyExistingInvalidFileIsNeverReplaced(t *testing.T) {
	for _, content := range []string{"", "short-private", "long-private\x00invalid-key", "long-private\ninvalid-key"} {
		path := filepath.Join(t.TempDir(), "service_key")
		require.NoError(t, os.WriteFile(path, []byte(content), 0600))
		before, err := os.Stat(path)
		require.NoError(t, err)
		_, err = New(automaticKeyConfig(path))
		require.Error(t, err)
		if content != "" {
			require.NotContains(t, err.Error(), content)
		}
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		require.True(t, string(after) == content)
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.True(t, os.SameFile(before, info))
	}
}

func TestServiceKeyUnreadableFileAndDanglingLinkAreNotReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service_key")
	const existing = "existing-manually-managed-key"
	require.NoError(t, os.WriteFile(path, []byte(existing), 0600))
	require.NoError(t, os.Chmod(path, 0000))
	if os.Geteuid() != 0 {
		require.Error(t, EnsureServiceKeyFile(path))
	}
	require.NoError(t, os.Chmod(path, 0600))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, string(raw) == existing)
	link := filepath.Join(t.TempDir(), "service_key")
	target := filepath.Join(t.TempDir(), "missing")
	require.NoError(t, os.Symlink(target, link))
	require.Error(t, EnsureServiceKeyFile(link))
	_, err = os.Stat(target)
	require.True(t, errors.Is(err, os.ErrNotExist))
}

func TestServiceKeyManualAndDisabledModesDoNotGenerateFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service_key")
	cfg := automaticKeyConfig(path)
	cfg.AutoGenerateServiceKey = false
	_, err := New(cfg)
	require.Error(t, err)
	_, err = os.Stat(path)
	require.True(t, errors.Is(err, os.ErrNotExist))
	cfg.AutoGenerateServiceKey = true
	cfg.Enabled = false
	client, err := New(cfg)
	require.NoError(t, err)
	require.Nil(t, client)
	_, err = os.Stat(path)
	require.True(t, errors.Is(err, os.ErrNotExist))
	const manual = "manually-provisioned-service-key"
	require.NoError(t, os.WriteFile(path, []byte(manual+"\r\n"), 0600))
	cfg.Enabled = true
	for _, automatic := range []bool{false, true} {
		cfg.AutoGenerateServiceKey = automatic
		client, err := New(cfg)
		require.NoError(t, err)
		require.True(t, client.key == manual)
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		require.True(t, strings.HasSuffix(string(raw), "\r\n"), "reuse must not rewrite the existing file")
	}
}
