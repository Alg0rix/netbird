//go:build e2e

package harness

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

// Signal is a standalone signaling broker for isolated handshake outage injection.
type Signal struct{ container testcontainers.Container }

// WithSignalAddress configures an external plaintext signal broker, such as
// http://signal.local:10000, instead of the combined process's embedded signal.
func WithSignalAddress(address string) CombinedOption {
	return func(o *combinedOptions) { o.signalURI = address }
}

// StartSignal builds and starts the current signal broker on the shared Docker network.
func StartSignal(ctx context.Context, c *Combined, name string) (*Signal, error) {
	if _, err := rp.CanonicalEndpoint(rp.RelayEndpoint("rel://" + name + ":10000")); err != nil || strings.ContainsAny(name, "/:") {
		return nil, fmt.Errorf("invalid signal container alias")
	}
	root, err := repoRoot(ctx)
	if err != nil {
		return nil, err
	}
	image, err := resolveImage(ctx, root, "NB_E2E_SIGNAL_IMAGE", defaultRelayImage, relayDockerfile)
	if err != nil {
		return nil, err
	}
	request := testcontainers.ContainerRequest{
		Image: image, Entrypoint: []string{"/usr/local/bin/netbird-signal"},
		Cmd:      []string{"run", "--port", "10000", "--log-file", "console", "--log-level", "info"},
		Networks: []string{c.network.Name}, NetworkAliases: map[string][]string{c.network.Name: {name}},
		ExposedPorts: []string{"10000/tcp"},
		WaitingFor:   wait.ForListeningPort("10000/tcp").WithStartupTimeout(30 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: request, Started: true})
	if err != nil {
		if container != nil {
			err = errors.Join(err, container.Terminate(context.WithoutCancel(ctx)))
		}
		return nil, err
	}
	return &Signal{container: container}, nil
}

// Stop interrupts signal streams while management and relay services continue running.
func (s *Signal) Stop(ctx context.Context) error { return s.container.Stop(ctx, nil) }

// Start restores the signal broker and waits for its listening port.
func (s *Signal) Start(ctx context.Context) error { return s.container.Start(ctx) }

// Logs returns signal diagnostics after a reset or failed handshake.
func (s *Signal) Logs(ctx context.Context) string { return containerLogs(ctx, s.container) }

// Terminate removes the disposable signal broker.
func (s *Signal) Terminate(ctx context.Context) error { return s.container.Terminate(ctx) }
