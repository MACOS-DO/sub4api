package config

import (
	"reflect"
	"testing"
)

func TestConfigDirectoryRenamePrecedence(t *testing.T) {
	t.Setenv("CONFIG_FILE", "")
	t.Setenv("DATA_DIR", "/custom/data")
	var paths []string
	configureConfigSource(func(string) { t.Fatal("unexpected explicit config") }, func(path string) { paths = append(paths, path) })
	want := []string{"/custom/data", "/app/data", ".", "./config", "/etc/sub4api", "/etc/sub2api"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	t.Setenv("CONFIG_FILE", "/legacy/custom.yaml")
	configureConfigSource(func(path string) {
		if path != "/legacy/custom.yaml" {
			t.Fatalf("explicit path = %s", path)
		}
	}, func(string) { t.Fatal("explicit config must override directory search") })
}
