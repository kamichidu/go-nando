package repository

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSettingsReadLegacyAndWriteCanonicalRemoteNames(t *testing.T) {
	var legacy Settings
	if err := json.Unmarshal([]byte(`{"delivery_mode":"gcs","gcs_bucket":"archive"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.DeliveryMode != DeliveryModeRemote || legacy.RemoteBackend != "gcs" || legacy.RemoteBucket != "archive" {
		t.Fatalf("legacy settings were not mapped to remote settings: %+v", legacy)
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"delivery_mode":"remote"`, `"remote_backend":"gcs"`, `"remote_bucket":"archive"`} {
		if !strings.Contains(string(encoded), key) {
			t.Errorf("canonical repository settings lack %s: %s", key, encoded)
		}
	}
	if strings.Contains(string(encoded), "gcs_bucket") {
		t.Fatalf("canonical repository settings retained a legacy key: %s", encoded)
	}
}

func TestSettingsRejectMultipleRemoteBackends(t *testing.T) {
	s := Settings{DeliveryMode: DeliveryModeRemote, RemoteBackend: "other", RemoteBucket: "bucket"}
	if err := s.Validate(); err == nil {
		t.Fatal("unsupported remote backend was accepted")
	}
}
