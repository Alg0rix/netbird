package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/netbirdio/netbird/shared/management/http/api"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

// RelayPoliciesAPI administers account relay groups, home profiles and symmetric pair policies.
type RelayPoliciesAPI struct{ c *Client }

// RelayListOptions bounds and filters relay resource listings.
type RelayListOptions struct {
	Limit  int
	Offset int
	Name   string
	PeerID rp.PeerID
}

func (options RelayListOptions) query() map[string]string {
	query := map[string]string{"offset": strconv.Itoa(options.Offset)}
	if options.Limit != 0 {
		query["limit"] = strconv.Itoa(options.Limit)
	}
	if options.Name != "" {
		query["name"] = options.Name
	}
	if options.PeerID != "" {
		query["peer_id"] = string(options.PeerID)
	}
	return query
}

func relayRequest[T any](ctx context.Context, c *Client, method, path string, request any, query map[string]string) (*T, error) {
	var body *bytes.Reader
	if request != nil {
		data, err := json.Marshal(request)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	var response *http.Response
	var err error
	if body == nil {
		response, err = c.NewRequest(ctx, method, path, nil, query)
	} else {
		response, err = c.NewRequest(ctx, method, path, body, query)
	}
	if err != nil {
		return nil, err
	}
	if response.Body != nil {
		defer response.Body.Close()
	}
	result, err := parseResponse[T](response)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// Catalog reads the published operator-approved catalog without exposing relay credentials.
func (a *RelayPoliciesAPI) Catalog(ctx context.Context) (*api.RelayCatalogResponse, error) {
	return relayRequest[api.RelayCatalogResponse](ctx, a.c, http.MethodGet, "/api/relay-catalog", nil, nil)
}

// Configuration reads account relay configuration and server feature support.
func (a *RelayPoliciesAPI) Configuration(ctx context.Context) (*api.RelayPolicyConfiguration, error) {
	return relayRequest[api.RelayPolicyConfiguration](ctx, a.c, http.MethodGet, "/api/relay-policies/configuration", nil, nil)
}

// SaveConfiguration atomically replaces configuration using its revision and catalog generation.
func (a *RelayPoliciesAPI) SaveConfiguration(ctx context.Context, request api.RelayPolicyConfiguration) (*api.RelayPolicyConfiguration, error) {
	return relayRequest[api.RelayPolicyConfiguration](ctx, a.c, http.MethodPut, "/api/relay-policies/configuration", request, nil)
}

// Preview resolves a draft without changing account configuration or granting connectivity.
func (a *RelayPoliciesAPI) Preview(ctx context.Context, request api.RelayPolicyConfiguration) (*api.RelayPolicyPreview, error) {
	return relayRequest[api.RelayPolicyPreview](ctx, a.c, http.MethodPost, "/api/relay-policies/preview", request, nil)
}

// Effective reads the symmetric pair policy, or the home profile when peerB is empty.
func (a *RelayPoliciesAPI) Effective(ctx context.Context, peerA, peerB rp.PeerID) (*api.RelayEffectiveResponse, error) {
	return relayRequest[api.RelayEffectiveResponse](ctx, a.c, http.MethodGet, "/api/relay-policies/effective", nil, map[string]string{"peer_a": string(peerA), "peer_b": string(peerB)})
}

// Status reads both authenticated endpoint observations separately from the desired home or pair policy.
func (a *RelayPoliciesAPI) Status(ctx context.Context, peerA, peerB rp.PeerID) (*api.RelayPolicyStatus, error) {
	return relayRequest[api.RelayPolicyStatus](ctx, a.c, http.MethodGet, "/api/relay-policies/status", nil, map[string]string{"peer_a": string(peerA), "peer_b": string(peerB)})
}

// RetryPreferred advances a targeted retry generation without changing relay permissions.
func (a *RelayPoliciesAPI) RetryPreferred(ctx context.Context, request api.RelayRetryRequest) (*api.RelayRetryResponse, error) {
	return relayRequest[api.RelayRetryResponse](ctx, a.c, http.MethodPost, "/api/relay-policies/retry-preferred", request, nil)
}

// ListGroups returns a bounded page of account relay groups.
func (a *RelayPoliciesAPI) ListGroups(ctx context.Context, options RelayListOptions) (*api.RelayGroupList, error) {
	return relayRequest[api.RelayGroupList](ctx, a.c, http.MethodGet, "/api/relay-groups", nil, options.query())
}

// GetGroup reads an account-owned relay group.
func (a *RelayPoliciesAPI) GetGroup(ctx context.Context, id rp.RelayGroupID) (*api.RelayGroup, error) {
	return relayRequest[api.RelayGroup](ctx, a.c, http.MethodGet, "/api/relay-groups/"+url.PathEscape(string(id)), nil, nil)
}

// CreateGroup creates a relay group with a server-assigned ID.
func (a *RelayPoliciesAPI) CreateGroup(ctx context.Context, request api.RelayGroupMutation) (*api.RelayPolicyConfiguration, error) {
	return relayRequest[api.RelayPolicyConfiguration](ctx, a.c, http.MethodPost, "/api/relay-groups", request, nil)
}

// UpdateGroup replaces an account relay group using optimistic concurrency.
func (a *RelayPoliciesAPI) UpdateGroup(ctx context.Context, id rp.RelayGroupID, request api.RelayGroupMutation) (*api.RelayPolicyConfiguration, error) {
	return relayRequest[api.RelayPolicyConfiguration](ctx, a.c, http.MethodPut, "/api/relay-groups/"+url.PathEscape(string(id)), request, nil)
}

// DeleteGroup deletes an unreferenced group using the current configuration and catalog revisions.
func (a *RelayPoliciesAPI) DeleteGroup(ctx context.Context, id rp.RelayGroupID, revision, generation uint64) (*api.RelayPolicyConfiguration, error) {
	return relayRequest[api.RelayPolicyConfiguration](ctx, a.c, http.MethodDelete, "/api/relay-groups/"+url.PathEscape(string(id)), nil, relayRevisionQuery(revision, generation))
}

// ListHomeProfiles returns a bounded page of home registration profiles.
func (a *RelayPoliciesAPI) ListHomeProfiles(ctx context.Context, options RelayListOptions) (*api.RelayHomeProfileList, error) {
	return relayRequest[api.RelayHomeProfileList](ctx, a.c, http.MethodGet, "/api/relay-home-profiles", nil, options.query())
}

// GetHomeProfile reads an account-owned home registration profile.
func (a *RelayPoliciesAPI) GetHomeProfile(ctx context.Context, id rp.RelayProfileID) (*api.RelayHomeProfile, error) {
	return relayRequest[api.RelayHomeProfile](ctx, a.c, http.MethodGet, "/api/relay-home-profiles/"+url.PathEscape(string(id)), nil, nil)
}

// CreateHomeProfile creates a home profile with a server-assigned ID.
func (a *RelayPoliciesAPI) CreateHomeProfile(ctx context.Context, request api.RelayHomeProfileMutation) (*api.RelayPolicyConfiguration, error) {
	return relayRequest[api.RelayPolicyConfiguration](ctx, a.c, http.MethodPost, "/api/relay-home-profiles", request, nil)
}

// UpdateHomeProfile replaces an account home profile using optimistic concurrency.
func (a *RelayPoliciesAPI) UpdateHomeProfile(ctx context.Context, id rp.RelayProfileID, request api.RelayHomeProfileMutation) (*api.RelayPolicyConfiguration, error) {
	return relayRequest[api.RelayPolicyConfiguration](ctx, a.c, http.MethodPut, "/api/relay-home-profiles/"+url.PathEscape(string(id)), request, nil)
}

// DeleteHomeProfile deletes an account home profile using optimistic concurrency.
func (a *RelayPoliciesAPI) DeleteHomeProfile(ctx context.Context, id rp.RelayProfileID, revision, generation uint64) (*api.RelayPolicyConfiguration, error) {
	return relayRequest[api.RelayPolicyConfiguration](ctx, a.c, http.MethodDelete, "/api/relay-home-profiles/"+url.PathEscape(string(id)), nil, relayRevisionQuery(revision, generation))
}

// ListPairPolicies returns policies visible from either matching endpoint.
func (a *RelayPoliciesAPI) ListPairPolicies(ctx context.Context, options RelayListOptions) (*api.RelayPairPolicyList, error) {
	return relayRequest[api.RelayPairPolicyList](ctx, a.c, http.MethodGet, "/api/relay-pair-policies", nil, options.query())
}

// GetPairPolicy reads an account-owned symmetric pair policy.
func (a *RelayPoliciesAPI) GetPairPolicy(ctx context.Context, id rp.PolicyID) (*api.RelayPairPolicy, error) {
	return relayRequest[api.RelayPairPolicy](ctx, a.c, http.MethodGet, "/api/relay-pair-policies/"+url.PathEscape(string(id)), nil, nil)
}

// CreatePairPolicy creates a symmetric policy with a server-assigned ID.
func (a *RelayPoliciesAPI) CreatePairPolicy(ctx context.Context, request api.RelayPairPolicyMutation) (*api.RelayPolicyConfiguration, error) {
	return relayRequest[api.RelayPolicyConfiguration](ctx, a.c, http.MethodPost, "/api/relay-pair-policies", request, nil)
}

// UpdatePairPolicy replaces an account pair policy using optimistic concurrency.
func (a *RelayPoliciesAPI) UpdatePairPolicy(ctx context.Context, id rp.PolicyID, request api.RelayPairPolicyMutation) (*api.RelayPolicyConfiguration, error) {
	return relayRequest[api.RelayPolicyConfiguration](ctx, a.c, http.MethodPut, "/api/relay-pair-policies/"+url.PathEscape(string(id)), request, nil)
}

// DeletePairPolicy deletes an account pair policy using optimistic concurrency.
func (a *RelayPoliciesAPI) DeletePairPolicy(ctx context.Context, id rp.PolicyID, revision, generation uint64) (*api.RelayPolicyConfiguration, error) {
	return relayRequest[api.RelayPolicyConfiguration](ctx, a.c, http.MethodDelete, "/api/relay-pair-policies/"+url.PathEscape(string(id)), nil, relayRevisionQuery(revision, generation))
}

func relayRevisionQuery(revision, generation uint64) map[string]string {
	return map[string]string{"expected_revision": strconv.FormatUint(revision, 10), "catalog_generation": strconv.FormatUint(generation, 10)}
}
