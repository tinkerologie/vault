package engine

import (
	"context"
	"io"

	"github.com/moby/moby/client"
)

// dockerClient is the narrow subset of the moby APIClient surface that the
// engine package consumes. Defined here so tests can supply a mock without
// having to satisfy the full *client.Client struct. The concrete moby
// *client.Client satisfies this interface implicitly (compile-time checked
// by the var-blank assertion below).
//
// Keep this list trimmed to the methods actually called in container.go /
// unraid_docker.go. Adding here is fine when a new call site lands, but
// removing requires a sweep to make sure no other call path depends on it.
type dockerClient interface {
	ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerInspect(ctx context.Context, container string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, container string, options client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerStop(ctx context.Context, container string, options client.ContainerStopOptions) (client.ContainerStopResult, error)
	ContainerRemove(ctx context.Context, container string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	ContainerStats(ctx context.Context, container string, options client.ContainerStatsOptions) (client.ContainerStatsResult, error)

	ImageInspect(ctx context.Context, image string, opts ...client.ImageInspectOption) (client.ImageInspectResult, error)
	ImageSave(ctx context.Context, images []string, opts ...client.ImageSaveOption) (client.ImageSaveResult, error)
	ImageLoad(ctx context.Context, input io.Reader, opts ...client.ImageLoadOption) (client.ImageLoadResult, error)
	ImagePull(ctx context.Context, ref string, options client.ImagePullOptions) (client.ImagePullResponse, error)
}

// Compile-time guard: the real moby client must satisfy our narrow seam.
// If moby drops or changes one of these signatures, this assertion catches
// it at build time instead of leaving the runtime call site to panic.
var _ dockerClient = (*client.Client)(nil)
