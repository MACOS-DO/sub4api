package migrations

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 上游 Sub2API 的迁移文件必须原样保留：库在 Sub4API 与上游之间切换时，
// migrations_runner 会按 checksum 校验已应用的迁移。需要调整时请新增 Sub4API 迁移。
// 清单由 scripts/sync-upstream-migration-checksums.sh 生成。
func TestUpstreamMigrationsAreUnmodified(t *testing.T) {
	f, err := os.Open("upstream_checksums.txt")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	entries := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		require.Lenf(t, fields, 2, "malformed line: %q", line)
		want, name := fields[0], fields[1]
		entries++

		content, err := FS.ReadFile(name)
		require.NoErrorf(t, err, "upstream migration %s is missing", name)
		sum := sha256.Sum256([]byte(strings.TrimSpace(string(content))))
		require.Equalf(t, want, hex.EncodeToString(sum[:]),
			"upstream migration %s was modified; restore it and add a new Sub4API migration instead", name)
	}
	require.NoError(t, scanner.Err())
	require.NotZero(t, entries)
}
