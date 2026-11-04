//go:build e2e

package harness

import (
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/url"
	"time"

	"github.com/docker/go-connections/nat"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Postgres owns a disposable database and network for management replica scenarios.
// Terminate all attached servers and clients before terminating it.
type Postgres struct {
	container   testcontainers.Container
	network     *testcontainers.DockerNetwork
	internalDSN string
	hostDSN     string
}

// StartPostgres starts a shared SQL store solely inside the Docker acceptance network.
func StartPostgres(ctx context.Context) (*Postgres, error) {
	networkFixture, err := network.New(ctx)
	if err != nil {
		return nil, err
	}
	password := rand.Text()
	request := testcontainers.ContainerRequest{
		Image:    "postgres:17-alpine",
		Networks: []string{networkFixture.Name}, NetworkAliases: map[string][]string{networkFixture.Name: {"relay-policy-db"}},
		ExposedPorts: []string{"5432/tcp"},
		Env:          map[string]string{"POSTGRES_USER": "netbird", "POSTGRES_PASSWORD": password, "POSTGRES_DB": "netbird"},
		WaitingFor:   wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(time.Minute),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: request, Started: true})
	if err != nil {
		if container != nil {
			err = errors.Join(err, container.Terminate(context.WithoutCancel(ctx)))
		}
		return nil, errors.Join(err, networkFixture.Remove(context.WithoutCancel(ctx)))
	}
	cleanup := func() error {
		return errors.Join(container.Terminate(context.WithoutCancel(ctx)), networkFixture.Remove(context.WithoutCancel(ctx)))
	}
	host, err := container.Host(ctx)
	if err != nil {
		return nil, errors.Join(err, cleanup())
	}
	port, err := container.MappedPort(ctx, nat.Port("5432/tcp"))
	if err != nil {
		return nil, errors.Join(err, cleanup())
	}
	internalDSN := url.URL{Scheme: "postgres", User: url.UserPassword("netbird", password), Host: "relay-policy-db:5432", Path: "/netbird", RawQuery: "sslmode=disable"}
	hostDSN := internalDSN
	hostDSN.Host = net.JoinHostPort(host, port.Port())
	return &Postgres{container: container, network: networkFixture, internalDSN: internalDSN.String(), hostDSN: hostDSN.String()}, nil
}

// Terminate removes the disposable database and its owned network.
func (p *Postgres) Terminate(ctx context.Context) error {
	return errors.Join(p.container.Terminate(ctx), p.network.Remove(ctx))
}
