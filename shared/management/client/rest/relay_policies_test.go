package rest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/netbirdio/netbird/shared/management/client/rest"
	"github.com/netbirdio/netbird/shared/management/http/api"
)

func TestRelayPoliciesSDKMutationPreservesFences(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method, "group creation uses its REST endpoint")
		assert.Equal(t, "/api/relay-groups", r.URL.Path, "SDK exposes the public group contract")
		var request api.RelayGroupMutation
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		assert.Equal(t, uint64(7), request.ExpectedRevision, "preserve the client's optimistic fence")
		assert.Equal(t, uint64(3), request.CatalogGeneration, "preserve the catalog the client previewed")
		assert.Equal(t, []string{"a", "b"}, request.Resource.RelayIds, "relay membership reaches the HTTP boundary")
		require.NoError(t, json.NewEncoder(w).Encode(api.RelayPolicyConfiguration{Revision: 8, CatalogGeneration: 3, Supported: true}))
	}))
	t.Cleanup(server.Close)
	client := rest.New(server.URL, "token")
	result, err := client.RelayPolicies.CreateGroup(context.Background(), api.RelayGroupMutation{ExpectedRevision: 7, CatalogGeneration: 3, Resource: api.RelayGroup{Name: "Region", RelayIds: []string{"a", "b"}}})
	require.NoError(t, err)
	assert.Equal(t, uint64(8), result.Revision, "caller receives the committed revision")
}

func TestRelayPoliciesSDKFiltersAndConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "y", r.URL.Query().Get("peer_id"), "reverse endpoint filter reaches the server")
		assert.Equal(t, "20", r.URL.Query().Get("limit"), "explicit pagination is preserved")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"message": "reload relay configuration", "code": 409}))
	}))
	t.Cleanup(server.Close)
	client := rest.New(server.URL, "token")
	result, err := client.RelayPolicies.ListPairPolicies(context.Background(), rest.RelayListOptions{PeerID: "y", Limit: 20})
	require.Error(t, err)
	var apiError *rest.APIError
	require.ErrorAs(t, err, &apiError)
	assert.Equal(t, http.StatusConflict, apiError.StatusCode, "callers can distinguish stale edits from validation errors")
	assert.Nil(t, result, "a conflict cannot return a success response")
}

func TestRelayPoliciesSDKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method, "status lookup is read-only")
		assert.Equal(t, "/api/relay-policies/status", r.URL.Path, "status uses the public endpoint")
		assert.Equal(t, "x", r.URL.Query().Get("peer_a"), "first endpoint reaches the server")
		assert.Equal(t, "y", r.URL.Query().Get("peer_b"), "second endpoint reaches the server")
		require.NoError(t, json.NewEncoder(w).Encode(api.RelayPolicyStatus{PeerA: "x", PeerB: "y", Application: api.RelayPolicyStatusApplication("pending"), Endpoints: []api.RelayPathObservation{}}))
	}))
	t.Cleanup(server.Close)
	result, err := rest.New(server.URL, "token").RelayPolicies.Status(context.Background(), "x", "y")
	require.NoError(t, err)
	assert.Equal(t, api.RelayPolicyStatusApplication("pending"), result.Application, "application state survives the typed SDK boundary")
	assert.False(t, result.Healthy, "pending configuration cannot claim path health")
}
