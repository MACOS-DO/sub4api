package envcompat

import (
	"os"
	"testing"
)

func TestLegacyAndExplicitNewEnvironment(t *testing.T) {
	const key = "SUB4API_COMPAT_TEST"
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUB2API_COMPAT_TEST", "legacy")
	if got := Getenv(key); got != "legacy" {
		t.Fatalf("legacy: %q", got)
	}
	t.Setenv(key, "new")
	if got := Getenv(key); got != "new" {
		t.Fatalf("new: %q", got)
	}
	t.Setenv(key, "")
	if got := Getenv(key); got != "" {
		t.Fatalf("explicit empty: %q", got)
	}
}
