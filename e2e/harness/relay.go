//go:build e2e

package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"gopkg.in/yaml.v3"

	"github.com/netbirdio/netbird/management/server/store"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

const (
	relayDockerfile   = "e2e/harness/Dockerfile.relay"
	defaultRelayImage = "netbird-relay:e2e"
	relayPort         = "33080"
	relaySecret       = "e2e-relay-secret" //nolint:gosec // Disposable Docker-only deployment secret.
)

// ControlledRelayCatalog returns distinct A, B and C identities on the test network.
func ControlledRelayCatalog() []rp.RelayCatalogEntry {
	entries := make([]rp.RelayCatalogEntry, 0, 3)
	for _, name := range []string{"relay-a", "relay-b", "relay-c"} {
		endpoint := rp.RelayEndpoint("rel://" + name + ":" + relayPort)
		entries = append(entries, rp.RelayCatalogEntry{ID: rp.RelayID(name), Endpoint: endpoint, InstanceEndpoint: endpoint, Active: true})
	}
	return entries
}

// Relay is an independently stoppable physical native relay container.
type Relay struct {
	container testcontainers.Container
	entry     rp.RelayCatalogEntry
}

// StartRelay builds the current relay and starts one concrete instance on the deployment network.
func StartRelay(ctx context.Context, c *Combined, name string) (*Relay, error) {
	endpoint, err := rp.CanonicalEndpoint(rp.RelayEndpoint("rel://" + name + ":" + relayPort))
	if err != nil || strings.ContainsAny(name, "./:") {
		return nil, fmt.Errorf("invalid relay container name")
	}
	root, err := repoRoot(ctx)
	if err != nil {
		return nil, err
	}
	image, err := resolveImage(ctx, root, "NB_E2E_RELAY_IMAGE", defaultRelayImage, relayDockerfile)
	if err != nil {
		return nil, err
	}
	request := testcontainers.ContainerRequest{
		Image:          image,
		Networks:       []string{c.network.Name},
		NetworkAliases: map[string][]string{c.network.Name: {name}},
		ExposedPorts:   []string{"9000/tcp", "9090/tcp"},
		Env: map[string]string{
			"NB_LISTEN_ADDRESS": ":" + relayPort, "NB_EXPOSED_ADDRESS": name + ":" + relayPort,
			"NB_AUTH_SECRET": relaySecret, "NB_LOG_LEVEL": "info", "NB_LOG_FILE": "console",
			"NB_HEALTH_LISTEN_ADDRESS": ":9000", "NB_METRICS_PORT": "9090",
		},
		WaitingFor: wait.ForHTTP("/health").WithPort("9000/tcp").WithStartupTimeout(45 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: request, Started: true})
	if err != nil {
		if container != nil {
			err = errors.Join(err, container.Terminate(context.WithoutCancel(ctx)))
		}
		return nil, fmt.Errorf("start relay %s: %w", name, err)
	}
	return &Relay{container: container, entry: rp.RelayCatalogEntry{ID: rp.RelayID(name), Endpoint: endpoint, InstanceEndpoint: endpoint, Active: true}}, nil
}

// Entry returns the operator catalog identity for this physical relay.
func (r *Relay) Entry() rp.RelayCatalogEntry { return r.entry }

// Stop interrupts relay sessions without removing its identity or container.
func (r *Relay) Stop(ctx context.Context) error { return r.container.Stop(ctx, nil) }

// Start restores a stopped relay and waits for its real relay health probe.
func (r *Relay) Start(ctx context.Context) error { return r.container.Start(ctx) }

// Logs returns relay handshake, session and disconnect diagnostics.
func (r *Relay) Logs(ctx context.Context) string { return containerLogs(ctx, r.container) }

// Metrics returns the physical relay's Prometheus counters, including transferred bytes.
func (r *Relay) Metrics(ctx context.Context) (string, error) {
	host, err := r.container.Host(ctx)
	if err != nil {
		return "", err
	}
	port, err := r.container.MappedPort(ctx, nat.Port("9090/tcp"))
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+":"+port.Port()+"/metrics", nil)
	if err != nil {
		return "", err
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("relay metrics status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	return string(body), err
}

// Terminate removes this relay after the acceptance fixture has finished.
func (r *Relay) Terminate(ctx context.Context) error { return r.container.Terminate(ctx) }

// WithRelayCatalog configures explicit managed identities and their legacy advertisements.
func WithRelayCatalog(entries []rp.RelayCatalogEntry) CombinedOption {
	copied := append([]rp.RelayCatalogEntry(nil), entries...)
	for i := range copied {
		copied[i].Aliases = append([]rp.RelayEndpoint(nil), copied[i].Aliases...)
	}
	return func(o *combinedOptions) { o.catalog = copied }
}

// WithCombinedName selects a unique management/signal alias on a shared test network.
func WithCombinedName(name string) CombinedOption { return func(o *combinedOptions) { o.alias = name } }

// WithSharedNetwork attaches an independent management instance to another deployment's network.
// Terminate the attached instance before terminating the network owner.
func WithSharedNetwork(other *Combined) CombinedOption {
	return func(o *combinedOptions) { o.shared = other }
}

// WithPostgres connects a management replica to the disposable shared PostgreSQL store.
func WithPostgres(postgres *Postgres) CombinedOption {
	return func(o *combinedOptions) { o.postgres = postgres }
}

func renderCombinedConfig(o combinedOptions, exposedURL string) (string, error) {
	cfg := fmt.Sprintf(combinedConfigYAML, exposedURL, !o.geolocation, containerIssuer)
	var document map[string]any
	if err := yaml.Unmarshal([]byte(cfg), &document); err != nil {
		return "", err
	}
	server := document["server"].(map[string]any)
	server["store"].(map[string]any)["encryptionKey"] = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	if o.syncVersion != nil {
		if *o.syncVersion < 0 || *o.syncVersion > 1 {
			return "", fmt.Errorf("invalid sync message version %d", *o.syncVersion)
		}
		server["supportedSyncMessageVersions"] = *o.syncVersion
	}
	if o.signalURI != "" {
		server["signalUri"] = o.signalURI
	}
	if len(o.catalog) > 0 {
		canonical, err := rp.ValidateCatalog(rp.RelayCatalog{Entries: o.catalog})
		if err != nil {
			return "", err
		}
		addresses := make([]string, 0, len(canonical.Entries))
		entries := make([]map[string]any, 0, len(canonical.Entries))
		for _, entry := range canonical.Entries {
			addresses = append(addresses, string(entry.Endpoint))
			entries = append(entries, map[string]any{"id": entry.ID, "endpoint": entry.Endpoint, "instanceEndpoint": entry.InstanceEndpoint, "aliases": entry.Aliases, "active": entry.Active, "legacy": entry.Legacy})
		}
		server["relays"] = map[string]any{"addresses": addresses, "credentialsTTL": "12h", "secret": relaySecret, "catalog": entries}
	}
	if o.postgres != nil {
		server["store"] = map[string]any{"engine": "postgres", "dsn": o.postgres.internalDSN, "encryptionKey": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
		server["authStore"] = map[string]any{"engine": "postgres", "dsn": o.postgres.internalDSN}
	}
	bytes, err := yaml.Marshal(document)
	return string(bytes), err
}

func (c *Combined) openCatalogStore(ctx context.Context) (*store.SqlStore, error) {
	if c.postgres != nil {
		return store.NewPostgresqlStore(ctx, c.postgres.hostDSN, nil, true)
	}
	return store.NewSqliteStore(ctx, filepath.Join(c.workDir, "data"), nil, true)
}

// CurrentRelayCatalog reads the live authoritative SQL publication, independently of replica caches.
func (c *Combined) CurrentRelayCatalog(ctx context.Context) (_ rp.RelayCatalog, resultErr error) {
	repository, err := c.openCatalogStore(ctx)
	if err != nil {
		return rp.RelayCatalog{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, repository.Close(context.WithoutCancel(ctx))) }()
	return repository.GetRelayCatalog(ctx)
}

// PublishRelayCatalog uses the real transactional operator publication path on the
// disposable SQL store. Running management replicas observe the new generation.
func (c *Combined) PublishRelayCatalog(ctx context.Context, entries []rp.RelayCatalogEntry, expectedGeneration uint64) (_ rp.RelayCatalog, resultErr error) {
	repository, err := c.openCatalogStore(ctx)
	if err != nil {
		return rp.RelayCatalog{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, repository.Close(context.WithoutCancel(ctx))) }()
	return repository.PublishRelayCatalog(ctx, rp.RelayCatalog{Entries: entries}, expectedGeneration)
}

// WithSyncMessageVersion selects the management negotiation ceiling: zero for
// legacy network maps and one for component envelopes.
func WithSyncMessageVersion(version int) CombinedOption {
	return func(o *combinedOptions) { copyVersion := version; o.syncVersion = &copyVersion }
}
