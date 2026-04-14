package engine

import (
	"testing"
	"time"
)

func TestHealthCheckResult_String(t *testing.T) {
	r := HealthCheckResult{
		ContainerName: "plex",
		Status:        "healthy",
		Duration:      3 * time.Second,
		Message:       "Docker HEALTHCHECK passed",
	}
	if r.ContainerName != "plex" {
		t.Errorf("unexpected name: %s", r.ContainerName)
	}
	if r.Status != "healthy" {
		t.Errorf("unexpected status: %s", r.Status)
	}
	if r.Duration != 3*time.Second {
		t.Errorf("unexpected duration: %v", r.Duration)
	}
	if r.Message != "Docker HEALTHCHECK passed" {
		t.Errorf("unexpected message: %s", r.Message)
	}
}
