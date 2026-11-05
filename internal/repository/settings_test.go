package repository

import (
	"encoding/json"
	"testing"
)

func TestSettingsReadCanonicalRepositoryJSON(t *testing.T) {
	tests := []struct {
		name string
		json string
		want Settings
	}{
		{"local", `{"delivery_mode":"local"}`, Settings{DeliveryMode: DeliveryModeLocal}},
		{"remote GCS", `{"delivery_mode":"remote","remote_backend":"gcs","remote_uri":"gs://archive","max_local_bytes":42}`, Settings{DeliveryMode: DeliveryModeRemote, RemoteBackend: "gcs", RemoteURI: "gs://archive", MaxLocalBytes: 42}},
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
				t.Fatalf("valid settings rejected: %v", err)
			}
		})
	}
	for _, raw := range []string{
		`{"delivery_mode":"gcs","gcs_bucket":"archive"}`,
		`{"delivery_mode":"remote","gcs_bucket":"archive"}`,
		`{"delivery_mode":"unsupported"}`,
		`{"delivery_mode":"remote","remote_backend":"other","remote_uri":"gs://archive"}`,
		`{"delivery_mode":"remote","remote_uri":"gs://archive","max_local_bytes":-1}`,
	} {
		t.Run("reject "+raw, func(t *testing.T) {
			var got Settings
			if err := json.Unmarshal([]byte(raw), &got); err == nil {
				if err := got.Validate(); err == nil {
					t.Fatalf("invalid settings accepted: %+v", got)
				}
			}
		})
	}
}

func TestSettingsRejectUnsupportedRemoteBackend(t *testing.T) {
	s := Settings{DeliveryMode: DeliveryModeRemote, RemoteBackend: "other", RemoteURI: "gs://bucket"}
	if err := s.Validate(); err == nil {
		t.Fatal("unsupported remote backend was accepted")
	}
}
