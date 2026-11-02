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

func TestSettingsReadRepositoryJSONCompatibilityCases(t *testing.T) {
	tests := []struct {
		name string
		json string
		want Settings
	}{
		{"oldest local settings", `{"delivery_mode":"local"}`, Settings{DeliveryMode: DeliveryModeLocal}},
		{"gcs mode and bucket", `{"delivery_mode":"gcs","gcs_bucket":"archive"}`, Settings{DeliveryMode: DeliveryModeRemote, RemoteBackend: "gcs", RemoteBucket: "archive"}},
		{"generic mode with legacy bucket", `{"delivery_mode":"remote","gcs_bucket":"archive"}`, Settings{DeliveryMode: DeliveryModeRemote, RemoteBucket: "archive"}},
		{"generic settings", `{"delivery_mode":"remote","remote_backend":"gcs","remote_bucket":"archive","max_local_bytes":42}`, Settings{DeliveryMode: DeliveryModeRemote, RemoteBackend: "gcs", RemoteBucket: "archive", MaxLocalBytes: 42}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got Settings
			if err := json.Unmarshal([]byte(test.json), &got); err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("settings=%+v, want %+v", got, test.want)
			}
			if err := got.Validate(); err != nil {
				t.Fatalf("valid compatibility settings rejected: %v", err)
			}
		})
	}
	for _, raw := range []string{
		`{"delivery_mode":"remote","remote_bucket":"new","gcs_bucket":"old"}`,
		`{"delivery_mode":"unsupported"}`,
		`{"delivery_mode":"remote","remote_backend":"other","remote_bucket":"archive"}`,
		`{"delivery_mode":"remote","remote_bucket":"archive","max_local_bytes":-1}`,
	} {
		t.Run("reject "+raw, func(t *testing.T) {
			var got Settings
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				return
			}
			if err := got.Validate(); err == nil {
				t.Fatalf("invalid settings accepted: %+v", got)
			}
		})
	}
}

func TestSettingsRejectMultipleRemoteBackends(t *testing.T) {
	s := Settings{DeliveryMode: DeliveryModeRemote, RemoteBackend: "other", RemoteBucket: "bucket"}
	if err := s.Validate(); err == nil {
		t.Fatal("unsupported remote backend was accepted")
	}
}
