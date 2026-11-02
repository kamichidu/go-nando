package repository

import (
	"encoding/json"
	"fmt"
)

// DeliveryMode selects the repository's durable delivery contract.
type DeliveryMode string

const (
	DeliveryModeLocal  DeliveryMode = "local"
	DeliveryModeRemote DeliveryMode = "remote"

	// DeliveryModeGCS is retained for source compatibility. Use DeliveryModeRemote.
	DeliveryModeGCS DeliveryMode = DeliveryModeRemote
)

// Settings are repository-owned configuration, distinct from per-user CLI config.
// A repository selects at most one remote object storage backend.
type Settings struct {
	DeliveryMode  DeliveryMode `json:"delivery_mode"`
	RemoteBackend string       `json:"remote_backend,omitempty"`
	RemoteBucket  string       `json:"remote_bucket,omitempty"`
	MaxLocalBytes int64        `json:"max_local_bytes,omitempty"`

	// GCSBucket accepts legacy in-process callers. It is never written to disk.
	GCSBucket string `json:"-"`
}

// UnmarshalJSON accepts the original GCS-specific repository settings format.
// New settings are written with generic remote-storage keys.
func (s *Settings) UnmarshalJSON(data []byte) error {
	type settingsJSON struct {
		DeliveryMode  DeliveryMode `json:"delivery_mode"`
		RemoteBackend string       `json:"remote_backend"`
		RemoteBucket  string       `json:"remote_bucket"`
		GCSBucket     string       `json:"gcs_bucket"`
		MaxLocalBytes int64        `json:"max_local_bytes"`
	}
	var value settingsJSON
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	if value.RemoteBucket != "" && value.GCSBucket != "" && value.RemoteBucket != value.GCSBucket {
		return fmt.Errorf("remote_bucket and legacy gcs_bucket disagree")
	}
	if value.DeliveryMode == "gcs" {
		value.DeliveryMode = DeliveryModeRemote
		if value.RemoteBackend == "" {
			value.RemoteBackend = "gcs"
		}
	}
	if value.RemoteBucket == "" {
		value.RemoteBucket = value.GCSBucket
	}
	*s = Settings{DeliveryMode: value.DeliveryMode, RemoteBackend: value.RemoteBackend, RemoteBucket: value.RemoteBucket, MaxLocalBytes: value.MaxLocalBytes}
	return nil
}

func (s Settings) MarshalJSON() ([]byte, error) {
	mode := s.DeliveryMode
	if mode == "gcs" {
		mode = DeliveryModeRemote
	}
	backend := s.RemoteBackend
	if mode == DeliveryModeRemote && backend == "" {
		backend = "gcs"
	}
	return json.Marshal(struct {
		DeliveryMode  DeliveryMode `json:"delivery_mode"`
		RemoteBackend string       `json:"remote_backend,omitempty"`
		RemoteBucket  string       `json:"remote_bucket,omitempty"`
		MaxLocalBytes int64        `json:"max_local_bytes,omitempty"`
	}{mode, backend, s.Bucket(), s.MaxLocalBytes})
}

// Bucket returns the selected remote bucket, including the legacy field alias.
func (s Settings) Bucket() string {
	if s.RemoteBucket != "" {
		return s.RemoteBucket
	}
	return s.GCSBucket
}

// Canonical returns the generic representation used for new repository files.
func (s Settings) Canonical() Settings {
	if s.DeliveryMode == "gcs" {
		s.DeliveryMode = DeliveryModeRemote
	}
	if s.RemoteBucket == "" {
		s.RemoteBucket = s.GCSBucket
	}
	s.GCSBucket = ""
	if s.DeliveryMode == DeliveryModeRemote && s.RemoteBackend == "" {
		s.RemoteBackend = "gcs"
	}
	return s
}

func DefaultSettings() Settings { return Settings{DeliveryMode: DeliveryModeLocal} }

func (s Settings) Validate() error {
	if s.MaxLocalBytes < 0 {
		return fmt.Errorf("local capacity limit must not be negative")
	}
	if s.RemoteBucket != "" && s.GCSBucket != "" && s.RemoteBucket != s.GCSBucket {
		return fmt.Errorf("remote bucket and legacy GCS bucket disagree")
	}
	if s.DeliveryMode == "gcs" { // Support legacy callers constructing the old value directly.
		s.DeliveryMode = DeliveryModeRemote
	}
	switch s.DeliveryMode {
	case DeliveryModeLocal:
		if s.Bucket() != "" || s.RemoteBackend != "" {
			return fmt.Errorf("local delivery mode must not configure remote object storage")
		}
	case DeliveryModeRemote:
		if s.Bucket() == "" {
			return fmt.Errorf("remote delivery mode requires a bucket")
		}
		if s.RemoteBackend != "" && s.RemoteBackend != "gcs" {
			return fmt.Errorf("unsupported remote object storage backend %q", s.RemoteBackend)
		}
	default:
		return fmt.Errorf("unsupported delivery mode %q", s.DeliveryMode)
	}
	return nil
}
