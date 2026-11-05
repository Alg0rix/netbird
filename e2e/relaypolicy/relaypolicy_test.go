//go:build e2e

package relaypolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clientstatus "github.com/netbirdio/netbird/client/status"
	"github.com/netbirdio/netbird/e2e/harness"
	"github.com/netbirdio/netbird/shared/management/client/rest"
	"github.com/netbirdio/netbird/shared/management/http/api"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

const convergeTimeout = 150 * time.Second
const servicePort = 18080

var fixtureSequence atomic.Uint64

type testPeer struct {
	id      rp.PeerID
	address netip.Addr
	client  *harness.Client
	marker  string
}
type fixture struct {
	t           *testing.T
	server      *harness.Combined
	api         *rest.Client
	prefix      string
	groupID     string
	extraGroups []string
	keyID       string
	peers       []*testPeer
	mode        harness.ClientMode
}

func newFixture(t *testing.T, s *harness.Combined, management *rest.Client, mode harness.ClientMode, count int, options ...harness.ClientOption) *fixture {
	t.Helper()
	return newFixtureInGroups(t, s, management, mode, count, nil, options...)
}

func newFixtureInGroups(t *testing.T, s *harness.Combined, management *rest.Client, mode harness.ClientMode, count int, autoGroups []string, options ...harness.ClientOption) *fixture {
	t.Helper()
	f := &fixture{t: t, server: s, api: management, mode: mode, prefix: fmt.Sprintf("relay-e2e-%d", fixtureSequence.Add(1))}
	t.Cleanup(f.cleanup)
	ctx := t.Context()
	group, err := management.Groups.Create(ctx, api.PostApiGroupsJSONRequestBody{Name: f.prefix})
	require.NoError(t, err)
	f.groupID = group.Id
	key, err := management.SetupKeys.Create(ctx, api.PostApiSetupKeysJSONRequestBody{Name: f.prefix, Type: "reusable", ExpiresIn: 3600, AutoGroups: append([]string{group.Id}, autoGroups...)})
	require.NoError(t, err)
	f.keyID = key.Id
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("%s-%d", f.prefix, i)
		opts := append([]harness.ClientOption{harness.WithClientName(name), harness.WithClientMode(mode), harness.WithClientEnv(map[string]string{"NB_ENABLE_NETSTACK_LOCAL_FORWARDING": "true"})}, options...)
		client, err := harness.StartClient(ctx, s, key.Key, opts...)
		require.NoError(t, err)
		peer := &testPeer{client: client, marker: "actual-overlay-bytes-" + name}
		f.peers = append(f.peers, peer)
		require.NoError(t, client.WaitConnected(ctx, 90*time.Second))
		require.Eventually(t, func() bool {
			peers, err := management.Peers.List(ctx)
			if err != nil {
				return false
			}
			for _, p := range peers {
				if p.Name == name {
					peer.id = rp.PeerID(p.Id)
					parsed, parseErr := netip.ParseAddr(p.Ip)
					if parseErr != nil {
						return false
					}
					peer.address = parsed.Unmap()
					return true
				}
			}
			return false
		}, time.Minute, time.Second, "registered peer must appear in its own account")
		code, out, err := client.Exec(ctx, "sh", "-ec", `mkdir -p /tmp/relay-policy-http
printf '%s' "$1" > /tmp/relay-policy-http/index.html
cat > /tmp/relay-policy-http/respond <<'HTTP'
#!/bin/sh
while IFS= read -r line; do
  [ "$line" = "$(printf '\r')" ] && break
done
printf 'HTTP/1.1 200 OK\r\nContent-Length: %s\r\nConnection: close\r\n\r\n' "$(wc -c < /tmp/relay-policy-http/index.html)"
cat /tmp/relay-policy-http/index.html
HTTP
chmod +x /tmp/relay-policy-http/respond
busybox nc -lk -p 18080 -e /tmp/relay-policy-http/respond > /tmp/relay-policy-http/server.log 2>&1 < /dev/null &`, "fixture", peer.marker)
		require.NoError(t, err)
		require.Zero(t, code, "start the actual destination service: %s", out)
	}
	return f
}

func (f *fixture) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if f.t.Failed() {
		for _, peer := range f.peers {
			f.t.Logf("%s client logs:\n%s", peer.marker, peer.client.Logs(ctx))
		}
	}
	for retry := 0; retry < 5; retry++ {
		config, err := f.api.RelayPolicies.Configuration(ctx)
		if !assert.NoError(f.t, err, "read cleanup CAS fence") {
			break
		}
		config.PairPolicies = filterPairs(config.PairPolicies, f.prefix)
		config.HomeProfiles = filterHomes(config.HomeProfiles, f.prefix)
		config.Groups = filterGroups(config.Groups, f.prefix)
		_, err = f.api.RelayPolicies.SaveConfiguration(ctx, *config)
		if err == nil {
			break
		}
		var apiErr *rest.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict {
			assert.NoError(f.t, err, "remove only this fixture's relay resources")
			break
		}
	}
	for _, peer := range f.peers {
		assert.NoError(f.t, peer.client.Terminate(ctx), "terminate container before deleting peer")
		if peer.id != "" {
			assert.NoError(f.t, f.api.Peers.Delete(ctx, string(peer.id)), "delete fixture peer")
		}
	}
	if f.keyID != "" {
		assert.NoError(f.t, f.api.SetupKeys.Delete(ctx, f.keyID), "delete fixture setup key")
	}
	for _, id := range f.extraGroups {
		assert.NoError(f.t, f.api.Groups.Delete(ctx, id), "remove fixture peer-group reference")
	}
	if f.groupID != "" {
		assert.NoError(f.t, f.api.Groups.Delete(ctx, f.groupID), "delete fixture group")
	}
}

func filterPairs(values []api.RelayPairPolicy, prefix string) []api.RelayPairPolicy {
	result := make([]api.RelayPairPolicy, 0, len(values))
	for _, v := range values {
		if !strings.HasPrefix(v.Name, prefix) {
			result = append(result, v)
		}
	}
	return result
}
func filterHomes(values []api.RelayHomeProfile, prefix string) []api.RelayHomeProfile {
	result := make([]api.RelayHomeProfile, 0, len(values))
	for _, v := range values {
		if !strings.HasPrefix(v.Name, prefix) {
			result = append(result, v)
		}
	}
	return result
}
func filterGroups(values []api.RelayGroup, prefix string) []api.RelayGroup {
	result := make([]api.RelayGroup, 0, len(values))
	for _, v := range values {
		if !strings.HasPrefix(v.Name, prefix) {
			result = append(result, v)
		}
	}
	return result
}
func selection(ids ...string) api.RelaySelection {
	return api.RelaySelection{RelayIds: ids, GroupIds: []string{}}
}
func constraints(force bool, ids ...string) api.RelayConstraints {
	allow := selection(ids...)
	tiers := make([]api.RelaySelection, 0, len(ids))
	for _, id := range ids {
		tiers = append(tiers, selection(id))
	}
	return api.RelayConstraints{Allow: &allow, Exclude: selection(), Preferences: tiers, ForceRelay: force}
}
func selector(id rp.PeerID) api.RelayPeerSelector {
	return api.RelayPeerSelector{PeerIds: []string{string(id)}, GroupIds: []string{}}
}

func (f *fixture) pin(a, b int, force bool, ids ...string) api.RelayPairPolicy {
	return api.RelayPairPolicy{Id: fmt.Sprintf("%s-pair-%d-%d", f.prefix, a, b), Name: fmt.Sprintf("%s-pair-%d-%d", f.prefix, a, b), Priority: 100, Enabled: true, Source: selector(f.peers[a].id), Destination: selector(f.peers[b].id), Constraints: constraints(force, ids...)}
}
func (f *fixture) save(mutate func(*api.RelayPolicyConfiguration)) *api.RelayPolicyConfiguration {
	f.t.Helper()
	config, err := f.api.RelayPolicies.Configuration(f.t.Context())
	require.NoError(f.t, err)
	mutate(config)
	preview, err := f.api.RelayPolicies.Preview(f.t.Context(), *config)
	require.NoError(f.t, err, "preview real authorized peers before writing")
	require.False(f.t, preview.Truncated, "acceptance fixture must fit the complete impact preview")
	saved, err := f.api.RelayPolicies.SaveConfiguration(f.t.Context(), *config)
	require.NoError(f.t, err, "persist a valid CAS-fenced configuration")
	return saved
}
func (f *fixture) bytes(a, b int) bool {
	code, body, err := f.peers[a].client.HTTP(f.t.Context(), "http://"+f.peers[b].address.String()+":"+strconv.Itoa(servicePort)+"/")
	return err == nil && code == http.StatusOK && body == f.peers[b].marker
}
func (f *fixture) waitBytes(a, b int) {
	f.t.Helper()
	require.Eventually(f.t, func() bool { return f.bytes(a, b) }, convergeTimeout, 2*time.Second, "actual destination bytes must cross the overlay")
}
func (f *fixture) noBytes(a, b int, duration time.Duration) {
	f.t.Helper()
	code, body, err := f.peers[b].client.Exec(f.t.Context(), "busybox", "wget", "-qO-", "http://127.0.0.1:18080/")
	require.NoError(f.t, err)
	require.Zero(f.t, code, "destination service must be alive before testing denied delivery")
	require.Equal(f.t, f.peers[b].marker, body, "destination service must still serve its expected bytes")
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		require.False(f.t, f.bytes(a, b), "forbidden pair must not deliver the destination marker")
		select {
		case <-f.t.Context().Done():
			f.t.Fatal("fixture cancelled")
		case <-time.After(time.Second):
		}
	}
}
func (f *fixture) waitUnmanagedDirect(a, b int) {
	f.t.Helper()
	require.Eventually(f.t, func() bool {
		if !f.bytes(a, b) {
			return false
		}
		for _, index := range []int{a, b} {
			code, raw, err := f.peers[index].client.Exec(f.t.Context(), "netbird", "status", "--json")
			var local clientstatus.OutputOverview
			if err != nil || code != 0 || json.Unmarshal([]byte(raw), &local) != nil || len(local.RelayPolicies) != 0 {
				return false
			}
			remote := b
			if index == b {
				remote = a
			}
			found := false
			for _, peer := range local.Peers.Details {
				if peer.IP == f.peers[remote].address.String() && peer.ConnType == "P2P" && peer.RelayPolicy == nil {
					found = true
				}
			}
			if !found {
				return false
			}
		}
		effective, err := f.api.RelayPolicies.Effective(f.t.Context(), f.peers[a].id, f.peers[b].id)
		return err == nil && effective.Policy == nil
	}, convergeTimeout, time.Second, "clearing must remove both local managed scopes and restore direct delivery without restarting clients")
}

func (f *fixture) status(a, b int) (*api.RelayPolicyStatus, error) {
	return f.api.RelayPolicies.Status(f.t.Context(), f.peers[a].id, f.peers[b].id)
}
func (f *fixture) waitPath(a, b int, transport api.RelayPolicyStatusTransport, relayID string) *api.RelayPolicyStatus {
	f.t.Helper()
	started := time.Now()
	var observed *api.RelayPolicyStatus
	var previousStatus string
	defer func() {
		if !f.t.Failed() {
			return
		}
		for _, index := range []int{a, b} {
			code, status, err := f.peers[index].client.Exec(context.Background(), "netbird", "status", "--json")
			f.t.Logf("endpoint %d daemon status (code=%d, error=%v): %s", index, code, err, status)
			code, routes, err := f.peers[index].client.Exec(context.Background(), "ip", "route")
			f.t.Logf("endpoint %d routes (code=%d, error=%v): %s", index, code, err, routes)
		}
	}()
	require.Eventually(f.t, func() bool {
		code, body, probeErr := f.peers[a].client.HTTP(f.t.Context(), "http://"+f.peers[b].address.String()+":"+strconv.Itoa(servicePort)+"/")
		status, err := f.status(a, b)
		encoded, _ := json.Marshal(status)
		if string(encoded) != previousStatus {
			f.t.Logf("pair status=%s status-error=%v HTTP-code=%d body=%q HTTP-error=%v", encoded, err, code, body, probeErr)
			previousStatus = string(encoded)
		}
		if err != nil || status.Policy == nil || !status.Healthy || status.Application != api.RelayPolicyStatusApplicationApplied || status.Transport != transport || status.RelayId != relayID || len(status.Endpoints) != 2 {
			return false
		}
		for _, endpoint := range status.Endpoints {
			if !endpoint.Current || endpoint.Digest != status.Policy.Digest || endpoint.Revision != status.Policy.Revision || endpoint.CatalogGeneration != status.Policy.CatalogGeneration || endpoint.ReselectionGeneration != status.Policy.ReselectionGeneration || endpoint.State != api.RelayPathObservationStateApplied {
				return false
			}
		}
		if transport == api.RelayPolicyStatusTransportNativeRelay && (status.Endpoints[0].Attempt == 0 || status.Endpoints[0].Attempt != status.Endpoints[1].Attempt) {
			return false
		}
		observed = status
		return true
	}, convergeTimeout, 2*time.Second, "both authenticated endpoint observations must match the actual working path")
	f.waitBytes(a, b)
	f.t.Logf("path=%s relay=%s revision=%d digest=%s catalog=%d attempt=%d convergence=%s", transport, relayID, observed.Policy.Revision, observed.Policy.Digest, observed.Policy.CatalogGeneration, observed.Endpoints[0].Attempt, time.Since(started))
	return observed
}

func modes(t *testing.T) []harness.ClientMode {
	t.Helper()
	raw := os.Getenv("NB_E2E_RELAY_MODES")
	if raw == "" {
		return []harness.ClientMode{harness.ClientModeKernel, harness.ClientModeUserspace, harness.ClientModeNetstack}
	}
	var result []harness.ClientMode
	for _, mode := range strings.Split(raw, ",") {
		result = append(result, harness.ClientMode(mode))
	}
	for _, mode := range result {
		require.Contains(t, []harness.ClientMode{harness.ClientModeKernel, harness.ClientModeUserspace, harness.ClientModeNetstack}, mode, "unknown acceptance mode")
	}
	return result
}

func TestForcedPairLeavesOtherDirectAndSupportsConcurrentPins(t *testing.T) {
	for _, version := range []int{0, 1} {
		t.Run(fmt.Sprintf("map-%d", version), func(t *testing.T) {
			configured, err := harness.StartCombined(t.Context(), harness.WithCombinedName(fmt.Sprintf("mode-management-%d", version)), harness.WithSharedNetwork(server), harness.WithRelayCatalog(harness.ControlledRelayCatalog()), harness.WithSignalAddress("http://signal.local:10000"), harness.WithSyncMessageVersion(version))
			require.NoError(t, err)
			t.Cleanup(func() { cleanupServer(configured) })
			_, err = configured.Bootstrap(t.Context())
			require.NoError(t, err)
			for _, mode := range modes(t) {
				t.Run(string(mode), func(t *testing.T) {
					f := newFixture(t, configured, configured.API(), mode, 3)
					f.save(func(c *api.RelayPolicyConfiguration) {
						c.PairPolicies = append(c.PairPolicies, f.pin(0, 1, true, "relay-a"), f.pin(0, 2, false, "relay-a", "relay-b"))
					})
					f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
					before := relaySentBytes(t, relays["relay-a"])
					for i := 0; i < 4; i++ {
						f.waitBytes(0, 1)
					}
					require.Eventually(t, func() bool { return relaySentBytes(t, relays["relay-a"]) > before }, 30*time.Second, time.Second, "bytes must be observed on the selected physical relay")
					f.waitPath(0, 2, api.RelayPolicyStatusTransportDirect, "")
					f.save(func(c *api.RelayPolicyConfiguration) {
						for i, p := range c.PairPolicies {
							if p.Id == f.pin(0, 2, false).Id {
								c.PairPolicies[i].Constraints = constraints(true, "relay-b")
							}
						}
					})
					f.waitPath(0, 2, api.RelayPolicyStatusTransportNativeRelay, "relay-b")
					f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
					code, raw, err := f.peers[0].client.Exec(t.Context(), "netbird", "status", "--json")
					require.NoError(t, err)
					require.Zero(t, code, "read daemon mode")
					var local struct {
						Kernel bool `json:"usesKernelInterface"`
					}
					require.NoError(t, json.Unmarshal([]byte(raw), &local))
					assert.Equal(t, mode == "kernel", local.Kernel, "the test must use its requested networking mode")
					// Reuse the established clients to cover live exclusion and failover in every form.
					f.save(func(c *api.RelayPolicyConfiguration) {
						for i, policy := range c.PairPolicies {
							if policy.Id == f.pin(0, 1, true).Id {
								c.PairPolicies[i].Constraints = constraints(true, "relay-b", "relay-a")
								c.PairPolicies[i].Constraints.Exclude = selection("relay-a")
								c.PairPolicies[i].Constraints.Preferences = []api.RelaySelection{selection("relay-b")}
							}
						}
					})
					f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-b")
					f.save(func(c *api.RelayPolicyConfiguration) {
						for i, policy := range c.PairPolicies {
							if policy.Id == f.pin(0, 1, true).Id {
								c.PairPolicies[i].Constraints = constraints(true, "relay-a", "relay-b")
							}
						}
					})
					f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
					require.NoError(t, relays["relay-a"].Stop(t.Context()))
					t.Cleanup(func() { assert.NoError(t, relays["relay-a"].Start(context.Background())) })
					fallback := f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-b")
					require.NoError(t, relays["relay-a"].Start(t.Context()))
					_, err = f.api.RelayPolicies.RetryPreferred(t.Context(), api.RelayRetryRequest{PeerA: string(f.peers[0].id), PeerB: ptr(string(f.peers[1].id)), ExpectedDigest: fallback.Policy.Digest})
					require.NoError(t, err)
					f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
					f.waitPath(0, 2, api.RelayPolicyStatusTransportNativeRelay, "relay-b")
					f.save(func(c *api.RelayPolicyConfiguration) { c.PairPolicies = filterPairs(c.PairPolicies, f.prefix) })
					f.waitUnmanagedDirect(0, 1)
					f.waitUnmanagedDirect(0, 2)
					f.save(func(c *api.RelayPolicyConfiguration) {
						c.HomeProfiles = append(c.HomeProfiles, api.RelayHomeProfile{Id: f.prefix + "-clear-home", Name: f.prefix + "-clear-home", Enabled: true, Priority: 100, Selector: selector(f.peers[0].id), Constraints: constraints(false, "relay-a")})
					})
					require.Eventually(t, func() bool {
						status, err := f.api.RelayPolicies.Status(t.Context(), f.peers[0].id, "")
						return err == nil && status.Policy != nil && status.Healthy && status.RelayId == "relay-a"
					}, convergeTimeout, time.Second, "home policy must apply before it is cleared")
					f.save(func(c *api.RelayPolicyConfiguration) { c.HomeProfiles = filterHomes(c.HomeProfiles, f.prefix) })
					f.waitUnmanagedDirect(0, 1)
				})
			}
		})
	}
}

func TestHomeProfilesDoNotRestrictForeignPairs(t *testing.T) {
	f := newFixture(t, server, server.API(), "userspace", 2)
	f.save(func(c *api.RelayPolicyConfiguration) {
		for i, id := range []string{"relay-a", "relay-b"} {
			c.HomeProfiles = append(c.HomeProfiles, api.RelayHomeProfile{Id: fmt.Sprintf("%s-home-%d", f.prefix, i), Name: fmt.Sprintf("%s-home-%d", f.prefix, i), Enabled: true, Priority: 100, Selector: selector(f.peers[i].id), Constraints: constraints(false, id)})
		}
	})
	for i, id := range []string{"relay-a", "relay-b"} {
		require.Eventually(t, func() bool {
			status, err := f.api.RelayPolicies.Status(t.Context(), f.peers[i].id, "")
			return err == nil && status.Healthy && status.RelayId == id
		}, convergeTimeout, time.Second, "separate home registration must converge")
	}
	// Global force is a local narrowing control; the pair remains unconfigured.
	for _, peer := range f.peers {
		code, out, err := peer.client.Exec(t.Context(), "sh", "-ec", `iptables -I OUTPUT -p udp -j DROP`)
		require.NoError(t, err)
		require.Zero(t, code, "block UDP inside the disposable container: %s", out)
		t.Cleanup(func() {
			_, _, err := peer.client.Exec(context.Background(), "iptables", "-D", "OUTPUT", "-p", "udp", "-j", "DROP")
			assert.NoError(t, err)
		})
	}
	f.waitBytes(0, 1)
	effective, err := f.api.RelayPolicies.Effective(t.Context(), f.peers[0].id, f.peers[1].id)
	require.NoError(t, err)
	assert.Nil(t, effective.Policy, "home profiles must not manufacture a pair restriction")
}

func TestFailoverStickyManualExclusionAndTargetedRetry(t *testing.T) {
	f := newFixture(t, server, server.API(), "userspace", 3)
	f.save(func(c *api.RelayPolicyConfiguration) {
		c.PairPolicies = append(c.PairPolicies, f.pin(0, 1, true, "relay-a", "relay-b"), f.pin(0, 2, true, "relay-b"))
	})
	f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
	stable := f.waitPath(0, 2, api.RelayPolicyStatusTransportNativeRelay, "relay-b")
	require.NoError(t, relays["relay-a"].Stop(t.Context()))
	t.Cleanup(func() { assert.NoError(t, relays["relay-a"].Start(context.Background())) })
	fallback := f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-b")
	require.NoError(t, relays["relay-a"].Start(t.Context()))
	// Keep observing through more than two report periods; recovered A cannot steal healthy B.
	deadline := time.Now().Add(2*rp.ReportInterval + 5*time.Second)
	for time.Now().Before(deadline) {
		f.waitBytes(0, 1)
		status, err := f.status(0, 1)
		require.NoError(t, err)
		assert.Equal(t, "relay-b", status.RelayId, "automatic fallback must remain sticky")
		time.Sleep(time.Second)
	}
	retry, err := f.api.RelayPolicies.RetryPreferred(t.Context(), api.RelayRetryRequest{PeerA: string(f.peers[0].id), PeerB: ptr(string(f.peers[1].id)), ExpectedDigest: fallback.Policy.Digest})
	require.NoError(t, err)
	require.Greater(t, retry.Generation, fallback.Policy.ReselectionGeneration, "targeted retry must advance generation")
	f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
	unchanged := f.waitPath(0, 2, api.RelayPolicyStatusTransportNativeRelay, "relay-b")
	assert.Equal(t, stable.Policy.Digest, unchanged.Policy.Digest, "retrying one pair must not reconcile another pair")
	f.save(func(c *api.RelayPolicyConfiguration) {
		for i, p := range c.PairPolicies {
			if p.Id == f.pin(0, 1, true).Id {
				c.PairPolicies[i].Constraints = constraints(true, "relay-b", "relay-a")
			}
		}
	})
	f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-b")
	f.save(func(c *api.RelayPolicyConfiguration) {
		for i, p := range c.PairPolicies {
			if p.Id == f.pin(0, 1, true).Id {
				next := constraints(true, "relay-a", "relay-b")
				next.Exclude = selection("relay-b")
				next.Preferences = []api.RelaySelection{selection("relay-a")}
				c.PairPolicies[i].Constraints = next
			}
		}
	})
	f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
	require.NoError(t, relays["relay-a"].Stop(t.Context()))
	require.NoError(t, relays["relay-b"].Stop(t.Context()))
	t.Cleanup(func() { assert.NoError(t, relays["relay-b"].Start(context.Background())) })
	require.Eventually(t, func() bool {
		status, err := f.status(0, 1)
		return err == nil && !status.Healthy && status.Transport != api.RelayPolicyStatusTransportNativeRelay
	}, convergeTimeout, time.Second, "exhausted allowed relays cannot remain healthy")
	f.noBytes(0, 1, 10*time.Second)
	status, err := f.status(0, 1)
	require.NoError(t, err)
	assert.NotEqual(t, "relay-c", status.RelayId, "unrelated healthy C must never become fallback")
}

func ptr[T any](value T) *T { return &value }

func TestOfflineAndRuntimeMembershipConflictCannotLookApplied(t *testing.T) {
	f := newFixture(t, server, server.API(), "userspace", 2)
	f.save(func(c *api.RelayPolicyConfiguration) {
		c.PairPolicies = append(c.PairPolicies, f.pin(0, 1, true, "relay-a"))
	})
	f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
	code, out, err := f.peers[1].client.Exec(t.Context(), "netbird", "down")
	require.NoError(t, err)
	require.Zero(t, code, "disconnect only disposable endpoint: %s", out)
	require.Eventually(t, func() bool {
		status, err := f.status(0, 1)
		return err == nil && !status.Healthy && status.Application != api.RelayPolicyStatusApplicationApplied
	}, convergeTimeout, time.Second, "offline endpoint invalidates old success")
	f.noBytes(0, 1, 5*time.Second)
	require.NoError(t, f.peers[1].client.Up(t.Context()))
	f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
	group, err := f.api.Groups.Create(t.Context(), api.PostApiGroupsJSONRequestBody{Name: f.prefix + "-conflict"})
	require.NoError(t, err)
	f.extraGroups = append(f.extraGroups, group.Id)
	f.save(func(c *api.RelayPolicyConfiguration) {
		for i, rule := range c.PairPolicies {
			if rule.Id == f.pin(0, 1, true).Id {
				c.PairPolicies[i].Source = api.RelayPeerSelector{PeerIds: []string{}, GroupIds: []string{f.groupID}}
			}
		}
		rule := f.pin(0, 1, true, "relay-b")
		rule.Id = f.prefix + "-inherited-conflict"
		rule.Name = rule.Id
		rule.Source = api.RelayPeerSelector{PeerIds: []string{}, GroupIds: []string{group.Id}}
		c.PairPolicies = append(c.PairPolicies, rule)
	})
	_, err = f.api.Groups.Update(t.Context(), group.Id, api.PutApiGroupsGroupIdJSONRequestBody{Name: group.Name, Peers: ptr([]string{string(f.peers[0].id)})})
	require.Error(t, err, "editable membership changes must reject an empty effective intersection")
	stored, err := f.api.Groups.Get(t.Context(), group.Id)
	require.NoError(t, err)
	assert.Empty(t, stored.Peers, "rejected activation must leave group membership unchanged")
	f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
	// Enrollment applies setup-key groups automatically, outside the editable mutation path.
	enrolled := newFixtureInGroups(t, server, server.API(), "userspace", 1, []string{f.groupID, group.Id})
	require.Eventually(t, func() bool {
		status, err := f.api.RelayPolicies.Status(t.Context(), enrolled.peers[0].id, f.peers[1].id)
		return err == nil && status.Policy != nil && status.Policy.State == api.RelayEffectivePolicyStateBlocked && !status.Healthy
	}, convergeTimeout, time.Second, "automatic enrollment into conflicting groups must report a blocked runtime policy")
	cross := &fixture{t: t, peers: []*testPeer{enrolled.peers[0], f.peers[1]}}
	cross.noBytes(0, 1, 5*time.Second)
	f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
	// Remove the reference before the peer-group cleanup callback runs.
	f.save(func(c *api.RelayPolicyConfiguration) {
		pairs := c.PairPolicies[:0]
		for _, p := range c.PairPolicies {
			if p.Id != f.prefix+"-inherited-conflict" {
				pairs = append(pairs, p)
			}
		}
		c.PairPolicies = pairs
	})
}

func TestSignalResetCannotApplyStalePath(t *testing.T) {
	f := newFixture(t, server, server.API(), "userspace", 2)
	f.save(func(c *api.RelayPolicyConfiguration) {
		c.PairPolicies = append(c.PairPolicies, f.pin(0, 1, true, "relay-a"))
	})
	old := f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
	require.NoError(t, signal.Stop(t.Context()))
	t.Cleanup(func() { assert.NoError(t, signal.Start(context.Background())) })
	f.save(func(c *api.RelayPolicyConfiguration) {
		for i, p := range c.PairPolicies {
			if p.Id == f.pin(0, 1, true).Id {
				c.PairPolicies[i].Constraints = constraints(true, "relay-b")
			}
		}
	})
	require.Eventually(t, func() bool {
		status, err := f.status(0, 1)
		return err == nil && status.Policy != nil && status.Policy.Digest != old.Policy.Digest && !status.Healthy && status.Application != api.RelayPolicyStatusApplicationApplied
	}, convergeTimeout, time.Second, "stale reports cannot promote a replacement while signal is unavailable")
	f.noBytes(0, 1, 5*time.Second)
	require.NoError(t, signal.Start(t.Context()))
	f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-b")
}

func TestIndependentAuthoritiesSharePhysicalRelaysWithoutConfiguration(t *testing.T) {
	other, err := harness.StartCombined(t.Context(), harness.WithCombinedName("management-independent"), harness.WithSharedNetwork(server), harness.WithRelayCatalog(harness.ControlledRelayCatalog()), harness.WithSignalAddress("http://signal.local:10000"))
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(other) })
	_, err = other.Bootstrap(t.Context())
	require.NoError(t, err)
	a := newFixture(t, server, server.API(), "userspace", 2)
	b := newFixture(t, other, other.API(), "userspace", 2)
	a.save(func(c *api.RelayPolicyConfiguration) {
		c.PairPolicies = append(c.PairPolicies, a.pin(0, 1, true, "relay-a"))
	})
	empty, err := b.api.RelayPolicies.Configuration(t.Context())
	require.NoError(t, err)
	assert.Empty(t, empty.PairPolicies, "another policy authority cannot inherit configuration through shared relay credentials")
	b.save(func(c *api.RelayPolicyConfiguration) {
		c.PairPolicies = append(c.PairPolicies, b.pin(0, 1, true, "relay-b"))
	})
	a.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
	b.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-b")
	_, err = a.api.Peers.Get(t.Context(), string(b.peers[1].id))
	require.Error(t, err, "foreign account peer IDs must not resolve")
	code, body, fetchErr := a.peers[0].client.HTTP(t.Context(), "http://"+b.peers[1].address.String()+":"+strconv.Itoa(servicePort)+"/")
	assert.False(t, fetchErr == nil && code == 200 && body == b.peers[1].marker, "shared HMAC credentials must not expose foreign overlay services")
}

func TestReplicasObserveAtomicCatalogReplacementAndRetirement(t *testing.T) {
	pg, err := harness.StartPostgres(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, pg.Terminate(context.Background())) })
	first, err := harness.StartCombined(t.Context(), harness.WithPostgres(pg), harness.WithCombinedName("replica-a"), harness.WithSignalAddress("http://replica-signal:10000"), harness.WithRelayCatalog(harness.ControlledRelayCatalog()))
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(first) })
	_, err = first.Bootstrap(t.Context())
	require.NoError(t, err)
	second, err := harness.StartCombined(t.Context(), harness.WithPostgres(pg), harness.WithCombinedName("replica-b"), harness.WithSignalAddress("http://replica-signal:10000"), harness.WithRelayCatalog(harness.ControlledRelayCatalog()))
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(second) })
	second.UsePAT(first.PAT)
	broker, err := harness.StartSignal(t.Context(), first, "replica-signal")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, broker.Terminate(context.Background())) })
	owned := make(map[rp.RelayID]*harness.Relay)
	for _, name := range []string{"relay-a", "relay-b", "relay-c", "relay-a-new"} {
		relay, err := harness.StartRelay(t.Context(), first, name)
		require.NoError(t, err)
		owned[rp.RelayID(name)] = relay
		t.Cleanup(func() { assert.NoError(t, relay.Terminate(context.Background())) })
	}
	f := newFixture(t, first, first.API(), "userspace", 2)
	// Move one existing identity to another replica without changing its local key.
	require.NoError(t, f.peers[1].client.Up(t.Context(), "--management-url", "http://replica-b:8080"))
	f.save(func(c *api.RelayPolicyConfiguration) {
		c.PairPolicies = append(c.PairPolicies, f.pin(0, 1, true, "relay-a"))
	})
	f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
	current, err := first.CurrentRelayCatalog(t.Context())
	require.NoError(t, err)
	replacement := owned["relay-a-new"].Entry()
	replacement.ID = "relay-a"
	entries := append([]rp.RelayCatalogEntry(nil), current.Entries...)
	for i, entry := range entries {
		if entry.ID == "relay-a" {
			entries[i] = replacement
		}
	}
	published, err := first.PublishRelayCatalog(t.Context(), entries, current.Generation)
	require.NoError(t, err)
	require.NoError(t, owned["relay-a"].Stop(t.Context()))
	converged := f.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
	assert.Equal(t, published.Generation, converged.Policy.CatalogGeneration, "both endpoint instructions must use the replacement publication")
	view, err := second.API().RelayPolicies.Status(t.Context(), f.peers[0].id, f.peers[1].id)
	require.NoError(t, err)
	require.NotNil(t, view.Policy)
	assert.Equal(t, converged.Policy.Digest, view.Policy.Digest, "a third read through another replica must agree")
	_, err = second.PublishRelayCatalog(t.Context(), current.Entries, current.Generation)
	require.Error(t, err, "stale publication cannot replace the active catalog")
	for i := range entries {
		if entries[i].ID == "relay-a" {
			entries[i].Active = false
		}
	}
	retired, err := second.PublishRelayCatalog(t.Context(), entries, published.Generation)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		status, err := f.status(0, 1)
		return err == nil && status.Policy != nil && status.Policy.CatalogGeneration == retired.Generation && status.Policy.State == api.RelayEffectivePolicyStateBlocked && !status.Healthy
	}, convergeTimeout, time.Second, "retirement must publish a blocked desired policy")
	require.Eventually(t, func() bool { return !f.bytes(0, 1) }, convergeTimeout, time.Second, "replicas must deliver retirement instructions and revoke the established path")
	f.noBytes(0, 1, 5*time.Second)
	require.NoError(t, second.Restart(t.Context()))
	after, err := second.CurrentRelayCatalog(t.Context())
	require.NoError(t, err)
	assert.Equal(t, retired.Generation, after.Generation, "restarting a stale replica must not republish its process configuration")
}

func TestOlderClientCompatibilityAndUnsupportedMembership(t *testing.T) {
	image := os.Getenv("NB_E2E_OLD_CLIENT_IMAGE")
	if image == "" {
		t.Skip("set NB_E2E_OLD_CLIENT_IMAGE to an Alpine-compatible released client without relay-policy capability; no old-binary result is claimed without it")
	}
	current := newFixture(t, server, server.API(), "userspace", 2)
	legacy := newFixture(t, server, server.API(), "userspace", 1, harness.WithClientImage(image))
	require.Eventually(t, func() bool {
		code, body, err := legacy.peers[0].client.HTTP(t.Context(), "http://"+current.peers[1].address.String()+":18080/")
		return err == nil && code == 200 && body == current.peers[1].marker
	}, convergeTimeout, time.Second, "old binary must still deliver bytes on an unconfigured pair")
	source, err := current.api.Groups.Create(t.Context(), api.PostApiGroupsJSONRequestBody{Name: current.prefix + "-supported-source", Peers: ptr([]string{string(current.peers[0].id)})})
	require.NoError(t, err)
	current.extraGroups = append(current.extraGroups, source.Id)
	current.save(func(c *api.RelayPolicyConfiguration) {
		rule := current.pin(0, 1, true, "relay-a")
		rule.Source = api.RelayPeerSelector{PeerIds: []string{}, GroupIds: []string{source.Id}}
		c.PairPolicies = append(c.PairPolicies, rule)
	})
	current.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
	_, err = current.api.Groups.Update(t.Context(), source.Id, api.PutApiGroupsGroupIdJSONRequestBody{Name: source.Name, Peers: ptr([]string{string(current.peers[0].id), string(legacy.peers[0].id)})})
	require.Error(t, err, "editable membership must reject an unsupported endpoint atomically")
	stored, err := current.api.Groups.Get(t.Context(), source.Id)
	require.NoError(t, err)
	require.Len(t, stored.Peers, 1, "rejected membership must retain only the supporting source")
	assert.Equal(t, string(current.peers[0].id), stored.Peers[0].Id, "the old endpoint must not enter the restricted scope")
	// A later enrollment can acquire this setup-key group without editable activation.
	unsupported := newFixtureInGroups(t, server, server.API(), "userspace", 1, []string{source.Id}, harness.WithClientImage(image))
	require.Eventually(t, func() bool {
		status, err := current.api.RelayPolicies.Status(t.Context(), unsupported.peers[0].id, current.peers[1].id)
		return err == nil && status.Application == api.RelayPolicyStatusApplicationUnsupported && !status.Healthy
	}, convergeTimeout, time.Second, "an older endpoint introduced into a restrictive scope must be reported unsupported")
	// Desired unsupported status is published before the asynchronous path teardown.
	require.Eventually(t, func() bool {
		code, body, fetchErr := unsupported.peers[0].client.HTTP(t.Context(), "http://"+current.peers[1].address.String()+":18080/")
		return fetchErr != nil || code != http.StatusOK || body != current.peers[1].marker
	}, convergeTimeout, time.Second, "mandatory unsupported restrictions must revoke the old unrestricted connection")
	cross := &fixture{t: t, peers: []*testPeer{unsupported.peers[0], current.peers[1]}}
	cross.noBytes(0, 1, 5*time.Second)
	current.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
	// Remove the cross-fixture reference before the older peer's cleanup runs.
	current.save(func(c *api.RelayPolicyConfiguration) { c.PairPolicies = filterPairs(c.PairPolicies, current.prefix) })
}

func TestOverlappingCustomerDatabaseAndHTTPRoutesStayIsolated(t *testing.T) {
	isolated, err := harness.StartCombined(t.Context(), harness.WithCombinedName("management-customers"), harness.WithSharedNetwork(server), harness.WithRelayCatalog(harness.ControlledRelayCatalog()), harness.WithSignalAddress("http://signal.local:10000"))
	require.NoError(t, err)
	t.Cleanup(func() { cleanupServer(isolated) })
	_, err = isolated.Bootstrap(t.Context())
	require.NoError(t, err)
	a := newFixture(t, isolated, isolated.API(), "userspace", 2)
	b := newFixture(t, isolated, isolated.API(), "userspace", 2)
	policies, err := isolated.API().Policies.List(t.Context())
	require.NoError(t, err)
	for _, policy := range policies {
		require.NotNil(t, policy.Id)
		require.NoError(t, isolated.API().Policies.Delete(t.Context(), *policy.Id), "discard only this disposable account's permissive default ACL")
	}
	for _, customer := range []*fixture{a, b} {
		groups := []string{customer.groupID}
		policy, err := isolated.API().Policies.Create(t.Context(), api.PostApiPoliciesJSONRequestBody{Name: customer.prefix, Enabled: true, Rules: []api.PolicyRuleUpdate{{Name: customer.prefix, Enabled: true, Action: api.PolicyRuleUpdateActionAccept, Protocol: api.PolicyRuleUpdateProtocolAll, Bidirectional: true, Sources: &groups, Destinations: &groups}}})
		require.NoError(t, err)
		require.NotNil(t, policy.Id)
		policyID := *policy.Id
		t.Cleanup(func() { assert.NoError(t, isolated.API().Policies.Delete(context.Background(), policyID)) })
		code, out, err := customer.peers[1].client.Exec(t.Context(), "ip", "address", "add", "10.77.0.1/32", "dev", "lo")
		require.NoError(t, err)
		require.Zero(t, code, "same private address belongs to separate disposable router namespaces: %s", out)
		route, err := isolated.API().Routes.Create(t.Context(), api.PostApiRoutesJSONRequestBody{Description: customer.prefix, Enabled: true, Groups: groups, Masquerade: true, Metric: 999, Network: ptr("10.77.0.1/32"), NetworkId: customer.prefix, Peer: ptr(string(customer.peers[1].id))})
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, isolated.API().Routes.Delete(context.Background(), route.Id)) })
	}
	a.save(func(c *api.RelayPolicyConfiguration) {
		c.PairPolicies = append(c.PairPolicies, a.pin(0, 1, true, "relay-a"), b.pin(0, 1, true, "relay-b"))
	})
	a.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
	b.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-b")
	const databaseImage = "postgres:17-alpine"
	// PostgreSQL shares only customer A's router namespace, including its private loopback.
	command := exec.CommandContext(t.Context(), "docker", "run", "-d", "--network", "container:"+a.peers[1].client.ContainerID(), "-e", "POSTGRES_PASSWORD=relay-fixture-password", databaseImage, "-c", "listen_addresses=10.77.0.1")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "start actual PostgreSQL sidecar: %s", output)
	databaseID := strings.TrimSpace(string(output))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, "docker", "rm", "-f", databaseID).CombinedOutput()
		assert.NoError(t, err, "remove PostgreSQL sidecar: %s", output)
	})
	query := func(source *testPeer) (string, error) {
		output, err := exec.CommandContext(t.Context(), "docker", "run", "--rm", "--network", "container:"+source.client.ContainerID(), "-e", "PGPASSWORD=relay-fixture-password", "-e", "PGCONNECT_TIMEOUT=3", databaseImage, "psql", "-h", "10.77.0.1", "-U", "postgres", "-Atqc", "SELECT 'customer-A-database-bytes'").CombinedOutput()
		return strings.TrimSpace(string(output)), err
	}
	require.Eventually(t, func() bool { body, err := query(a.peers[0]); return err == nil && body == "customer-A-database-bytes" }, convergeTimeout, time.Second, "authorized customer A SQL bytes must traverse its relay-backed route")
	require.Eventually(t, func() bool {
		code, body, err := b.peers[0].client.HTTP(t.Context(), "http://10.77.0.1:18080/")
		return err == nil && code == 200 && body == b.peers[1].marker
	}, convergeTimeout, time.Second, "customer B's identical private address must reach its own HTTP service")
	body, err := query(b.peers[0])
	assert.True(t, err != nil || body != "customer-A-database-bytes", "customer B cannot reach customer A database through an overlapping address")
	code, body, fetchErr := a.peers[0].client.HTTP(t.Context(), "http://"+b.peers[1].address.String()+":18080/")
	assert.False(t, fetchErr == nil && code == 200 && body == b.peers[1].marker, "relay policy must not bypass cross-customer ACL denial")
	a.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-a")
	b.waitPath(0, 1, api.RelayPolicyStatusTransportNativeRelay, "relay-b")
}

func relaySentBytes(t *testing.T, relay *harness.Relay) float64 {
	t.Helper()
	body, err := relay.Metrics(t.Context())
	require.NoError(t, err)
	var total float64
	found := false
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.SplitN(fields[0], "{", 2)[0]
		if !strings.HasSuffix(name, "relay_transfer_sent_bytes_total") {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		require.NoError(t, err)
		total += value
		found = true
	}
	require.True(t, found, "source-built relay must expose actual forwarded byte counters")
	return total
}
