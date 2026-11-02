package repository

import (
	"fmt"
)

// DeliveryMode selects the repository's durable delivery contract.
type DeliveryMode string

const (
	DeliveryModeLocal DeliveryMode = "local"
	DeliveryModeGCS   DeliveryMode = "gcs"
)

// Settings are repository-owned configuration, distinct from per-user CLI config.
type Settings struct {
	DeliveryMode  DeliveryMode `json:"delivery_mode"`
	GCSBucket     string       `json:"gcs_bucket,omitempty"`
	MaxLocalBytes int64        `json:"max_local_bytes,omitempty"`
}

func DefaultSettings() Settings { return Settings{DeliveryMode: DeliveryModeLocal} }

func (s Settings) Validate() error {
	if s.MaxLocalBytes < 0 {
		return fmt.Errorf("local capacity limit must not be negative")
	}
	switch s.DeliveryMode {
	case DeliveryModeLocal:
		if s.GCSBucket != "" {
			return fmt.Errorf("local delivery mode must not configure a GCS bucket")
		}
	case DeliveryModeGCS:
		if s.GCSBucket == "" {
			return fmt.Errorf("GCS delivery mode requires a bucket")
		}
	default:
		return fmt.Errorf("unsupported delivery mode %q", s.DeliveryMode)
	}
	return nil
}
