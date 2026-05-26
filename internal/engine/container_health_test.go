package engine

import (
	"testing"
	"time"
)

// VerifyContainerHealth: when the timeout expires before the 2s ticker
// fires (i.e. no Docker call is ever made), the function returns a
// "failed" HealthCheckResult with a timeout message. This is the timeout
// branch of the polling loop — it runs without ever contacting the
// daemon, so it's safe on hosts without Docker.
func TestVerifyContainerHealthTimeoutBranch(t *testing.T) {
	t.Parallel()

	// 1ms timeout — context done fires immediately, well before the 2s tick.
	res, err := VerifyContainerHealth("any-id", "any-name", 1*time.Millisecond)
	if err != nil {
		t.Fatalf("VerifyContainerHealth error = %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil HealthCheckResult on timeout branch")
	}
	if res.ContainerName != "any-name" {
		t.Fatalf("ContainerName = %q, want any-name", res.ContainerName)
	}
	if res.Status != "failed" {
		t.Fatalf("Status = %q, want failed (timeout path)", res.Status)
	}
	if res.Message == "" {
		t.Fatal("expected non-empty timeout message")
	}
}

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
