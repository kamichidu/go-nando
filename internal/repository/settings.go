package repository

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/kamichidu/go-nando/internal/objectstore/gcs"
)

// DeliveryMode selects the repository's durable delivery contract.
type DeliveryMode string

const (
	DeliveryModeLocal  DeliveryMode = "local"
	DeliveryModeRemote DeliveryMode = "remote"
)

// Settings are repository-owned configuration, distinct from per-user CLI config.
// A repository selects at most one remote object storage backend.
type Settings struct {
	DeliveryMode  DeliveryMode `json:"delivery_mode"`
	RemoteBackend string       `json:"remote_backend,omitempty"`
	RemoteURI     string       `json:"remote_uri,omitempty"`
	MaxLocalBytes int64        `json:"max_local_bytes,omitempty"`
}

func (s *Settings) UnmarshalJSON(data []byte) error {
	type settingsJSON Settings
	var decoded settingsJSON
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*s = Settings(decoded)
	return nil
}

func DefaultSettings() Settings { return Settings{DeliveryMode: DeliveryModeLocal} }

func (s Settings) Validate() error {
	if s.MaxLocalBytes < 0 {
		return fmt.Errorf("local capacity limit must not be negative")
	}
	switch s.DeliveryMode {
	case DeliveryModeLocal:
		if s.RemoteURI != "" || s.RemoteBackend != "" {
			return fmt.Errorf("local delivery mode must not configure remote object storage")
		}
	case DeliveryModeRemote:
		if s.RemoteURI == "" {
			return fmt.Errorf("remote delivery mode requires a remote URI")
		}
		if _, _, err := gcs.ParseRemoteURI(s.RemoteURI); err != nil {
			return fmt.Errorf("invalid remote URI: %w", err)
		}
		if s.RemoteBackend != "" && s.RemoteBackend != "gcs" {
			return fmt.Errorf("unsupported remote object storage backend %q", s.RemoteBackend)
		}
	default:
		return fmt.Errorf("unsupported delivery mode %q", s.DeliveryMode)
	}
	return nil
}
