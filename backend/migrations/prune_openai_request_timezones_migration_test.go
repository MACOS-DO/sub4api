package migrations

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration242PrunesUnsupportedOpenAIRequestTimezones(t *testing.T) {
	content, err := FS.ReadFile("242_prune_openai_request_timezones.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "SET extra = extra - 'openai_request_timezone'")
	require.Contains(t, sql, "WHERE platform = 'openai'")
	require.Contains(t, sql, "extra ? 'openai_request_timezone'")
	require.Contains(t, sql, "jsonb_typeof(extra->'openai_request_timezone') IS DISTINCT FROM 'string'")
	require.Contains(t, sql, "extra->>'openai_request_timezone' NOT IN (")

	list, err := os.ReadFile("../internal/service/openai_request_timezones.txt")
	require.NoError(t, err)
	matches := regexp.MustCompile(`(?m)^\s+'([^']+)'[,]?$`).FindAllStringSubmatch(sql, -1)
	options := make([]string, 0, len(matches))
	for _, match := range matches {
		options = append(options, match[1])
	}
	require.Len(t, options, 30)
	require.ElementsMatch(t, strings.Fields(string(list)), options)
}
