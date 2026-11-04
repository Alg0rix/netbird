package relaypolicies

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"runtime"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	nbcontext "github.com/netbirdio/netbird/management/server/context"
	nbpeer "github.com/netbirdio/netbird/management/server/peer"
	"github.com/netbirdio/netbird/management/server/permissions"
	"github.com/netbirdio/netbird/management/server/relaypolicy/admin"
	"github.com/netbirdio/netbird/management/server/store"
	"github.com/netbirdio/netbird/management/server/types"
	"github.com/netbirdio/netbird/shared/auth"
	"github.com/netbirdio/netbird/shared/management/http/api"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

func relayHTTPFixture(t *testing.T) (*mux.Router, *admin.Manager) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("SQLite store is not supported on Windows")
	}
	ctx := context.Background()
	s, cleanup, err := store.NewTestStoreFromSQL(ctx, "", t.TempDir())
	require.NoError(t, err)
	t.Cleanup(cleanup)
	account := &types.Account{Id: "relay-http", Network: types.NewNetwork(), Settings: &types.Settings{}, Users: map[string]*types.User{}, Peers: map[string]*nbpeer.Peer{}}
	for id, role := range map[string]types.UserRole{"owner": types.UserRoleOwner, "auditor": types.UserRoleAuditor, "regular": types.UserRoleUser} {
		account.Users[id] = &types.User{Id: id, AccountID: account.Id, Role: role}
	}
	for id, ip := range map[string]string{"x": "100.64.0.1", "y": "100.64.0.2"} {
		account.Peers[id] = &nbpeer.Peer{ID: id, AccountID: account.Id, Key: id, IP: netip.MustParseAddr(ip), Status: &nbpeer.PeerStatus{}, Meta: nbpeer.PeerSystemMeta{Capabilities: []int32{rp.Capability}}}
	}
	require.NoError(t, account.AddAllGroup(false))
	for _, policy := range account.Policies {
		policy.AccountID = account.Id
		for _, rule := range policy.Rules {
			rule.PolicyID = policy.ID
		}
	}
	require.NoError(t, s.SaveAccount(ctx, account))
	repository, ok := s.(store.RelayPolicyStore)
	require.True(t, ok, "HTTP integration requires the real SQL policy store")
	_, err = repository.InitializeRelayCatalog(ctx, rp.RelayCatalog{Entries: []rp.RelayCatalogEntry{{ID: "a", Endpoint: "rels://a.example:443", Active: true}, {ID: "b", Endpoint: "rels://b.example:443", Active: true}}})
	require.NoError(t, err)
	manager := admin.NewManager(s, permissions.NewManager(s), admin.Hooks{})
	h := &handler{manager: manager, store: s}
	router := mux.NewRouter()
	h.registerEndpoints(router)
	return router, manager
}

func relayHTTPRequest(t *testing.T, router http.Handler, user, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buffer bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buffer).Encode(body))
	}
	request := httptest.NewRequest(method, path, &buffer)
	request = nbcontext.SetUserAuthInRequest(request, auth.UserAuth{AccountId: "relay-http", UserId: user})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestRelayHTTPGroupCRUDAndStaleEdit(t *testing.T) {
	router, _ := relayHTTPFixture(t)
	request := api.RelayGroupMutation{ExpectedRevision: 0, CatalogGeneration: 1, Resource: api.RelayGroup{Name: "Region", RelayIds: []string{"a", "b"}}}
	response := relayHTTPRequest(t, router, "owner", http.MethodPost, "/relay-groups", request)
	require.Equal(t, http.StatusOK, response.Code, "group creation should commit a typed resource: %s", response.Body.String())
	var saved api.RelayPolicyConfiguration
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &saved))
	require.Len(t, saved.Groups, 1)
	assert.NotEmpty(t, saved.Groups[0].Id, "server assigns a stable group identity")
	assert.Equal(t, uint64(1), saved.Revision, "creation advances revision")
	response = relayHTTPRequest(t, router, "owner", http.MethodGet, "/relay-groups?limit=1&name=REGION", nil)
	require.Equal(t, http.StatusOK, response.Code, "listing reads the persisted resource")
	var list api.RelayGroupList
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &list))
	assert.Equal(t, saved.Groups, list.Items, "read-back preserves relay membership and entity revision")
	request.Resource = saved.Groups[0]
	request.Resource.Name = "Stale"
	response = relayHTTPRequest(t, router, "owner", http.MethodPut, "/relay-groups/"+request.Resource.Id, request)
	assert.Equal(t, http.StatusConflict, response.Code, "a stale account revision must not overwrite an intervening mutation")
	response = relayHTTPRequest(t, router, "owner", http.MethodDelete, "/relay-groups/"+request.Resource.Id+"?expected_revision=1&catalog_generation=1", nil)
	assert.Equal(t, http.StatusOK, response.Code, "unreferenced groups can be deleted with current fences")
}

func TestRelayHTTPAuthorizationAndSymmetricEffectivePolicy(t *testing.T) {
	router, manager := relayHTTPFixture(t)
	response := relayHTTPRequest(t, router, "regular", http.MethodGet, "/relay-catalog", nil)
	assert.Equal(t, http.StatusForbidden, response.Code, "ordinary users cannot enumerate administrative relay configuration")
	response = relayHTTPRequest(t, router, "auditor", http.MethodPost, "/relay-groups", api.RelayGroupMutation{CatalogGeneration: 1, Resource: api.RelayGroup{Name: "Denied", RelayIds: []string{"a"}}})
	assert.Equal(t, http.StatusForbidden, response.Code, "auditor read permission does not permit mutations")
	candidate := rp.Configuration{Policies: []rp.RelayPairPolicy{{ID: "pair", AccountID: "relay-http", Name: "Pair", Enabled: true, Source: rp.RelaySelector{PeerIDs: []rp.PeerID{"x"}}, Destination: rp.RelaySelector{PeerIDs: []rp.PeerID{"y"}}, Constraints: rp.RelayConstraints{Allow: &rp.RelaySelection{RelayIDs: []rp.RelayID{"a"}}, ForceRelay: true}}}}
	_, err := manager.Save(context.Background(), "relay-http", "owner", 0, 1, candidate)
	require.NoError(t, err)
	var forward, reverse api.RelayEffectiveResponse
	response = relayHTTPRequest(t, router, "owner", http.MethodGet, "/relay-policies/effective?peer_a=x&peer_b=y", nil)
	require.Equal(t, http.StatusOK, response.Code, "authorized pair has effective policy: %s", response.Body.String())
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &forward))
	response = relayHTTPRequest(t, router, "owner", http.MethodGet, "/relay-policies/effective?peer_a=y&peer_b=x", nil)
	require.Equal(t, http.StatusOK, response.Code, "reverse pair lookup is authorized")
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &reverse))
	require.NotNil(t, forward.Policy)
	assert.Equal(t, forward, reverse, "either endpoint exposes the same policy digest and ordered relay choices")
	response = relayHTTPRequest(t, router, "owner", http.MethodGet, "/relay-pair-policies?peer_id=y", nil)
	require.Equal(t, http.StatusOK, response.Code, "reverse endpoint filtering includes its policy")
	var list api.RelayPairPolicyList
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &list))
	assert.Len(t, list.Items, 1, "pair policy is visible from its destination as well as its source")
}
