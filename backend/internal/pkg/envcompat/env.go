// Package envcompat resolves renamed environment variables without masking legacy configuration.
package envcompat

import (
	"os"
	"strings"
)

// Getenv honors an explicitly set new value, including an empty value, before the legacy alias.
func Getenv(key string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return os.Getenv(strings.ReplaceAll(key, "SUB4API", "SUB2API"))
}
