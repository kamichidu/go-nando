package main

import "testing"

func TestValidateLoopbackAddress(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8080", "[::1]:8080", "localhost:8080"} {
		t.Run("accept_"+address, func(t *testing.T) {
			if err := validateLoopbackAddress(address); err != nil {
				t.Fatalf("loopback address rejected: %v", err)
			}
		})
	}
	for _, address := range []string{"0.0.0.0:8080", "192.0.2.1:8080", "example.com:8080", "localhost.evil:8080", "8080"} {
		t.Run("reject_"+address, func(t *testing.T) {
			if err := validateLoopbackAddress(address); err == nil {
				t.Fatalf("non-loopback address %q accepted", address)
			}
		})
	}
}
