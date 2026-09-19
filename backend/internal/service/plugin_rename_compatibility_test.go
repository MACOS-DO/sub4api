package service

import (
	"encoding/json"
	"testing"
)

func TestPluginLegacyRequirements(t *testing.T) {
	var requirements PluginRequirements
	if err := json.Unmarshal([]byte(`{"sub2api":">=0.2.0","tested_sub2api_versions":["0.2.7"]}`), &requirements); err != nil {
		t.Fatal(err)
	}
	if requirements.Sub4API != ">=0.2.0" || len(requirements.TestedSub4APIVersions) != 1 {
		t.Fatalf("legacy manifest lost: %+v", requirements)
	}
	if err := json.Unmarshal([]byte(`{"sub2api":">=0.2.0","sub4api":"","tested_sub2api_versions":["0.2.7"],"tested_sub4api_versions":[]}`), &requirements); err != nil {
		t.Fatal(err)
	}
	if requirements.Sub4API != "" || len(requirements.TestedSub4APIVersions) != 0 {
		t.Fatalf("explicit new fields did not win: %+v", requirements)
	}
}
