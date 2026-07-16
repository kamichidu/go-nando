package config

import (
	"encoding/json"
	"os"
)

// Config represents the parsed contents of a nando configuration file.
type Config struct {
	Repository string `json:"repository"`
	Verbose    bool   `json:"verbose"`
}

// LoadConfig reads and decodes a JSON-formatted configuration file.
func LoadConfig(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var cfg Config
	if err := json.NewDecoder(file).Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
