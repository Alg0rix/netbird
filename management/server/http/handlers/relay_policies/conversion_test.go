package relaypolicies

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/netbirdio/netbird/shared/management/http/api"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

func TestRelayConstraintsPreserveAllowPresence(t *testing.T) {
	for _, body := range []string{`{"exclude":{"relay_ids":[],"group_ids":[]},"preferences":[],"force_relay":false}`, `{"allow":{"relay_ids":[],"group_ids":[]},"exclude":{"relay_ids":[],"group_ids":[]},"preferences":[],"force_relay":false}`} {
		var request api.RelayConstraints
		require.NoError(t, json.Unmarshal([]byte(body), &request))
		converted := fromConstraints(request)
		assert.Equal(t, strings.Contains(body, `"allow"`), converted.Allow != nil, "omitted and explicit empty allow must remain distinguishable")
		encoded, err := json.Marshal(constraints(converted))
		require.NoError(t, err)
		assert.JSONEq(t, body, string(encoded), "REST round trip preserves the restriction rather than broadening it")
	}
}

func TestRelayEffectiveResponseDistinguishesAbsentAndBlocked(t *testing.T) {
	absent, err := json.Marshal(api.RelayEffectiveResponse{Policy: effective(nil)})
	require.NoError(t, err)
	blocked, err := json.Marshal(api.RelayEffectiveResponse{Policy: effective(&rp.ResolvedRelayPolicy{State: rp.RelayPolicyBlocked, ForceRelay: true})})
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(absent), "only an absent policy preserves legacy behavior")
	assert.Contains(t, string(blocked), `"state":"blocked"`, "blocked policy is explicitly present")
	assert.Contains(t, string(blocked), `"force_relay":true`, "exhaustion cannot erase mandatory transport restriction")
}

func TestRelayRequestRejectsUnknownAndTrailingJSON(t *testing.T) {
	for _, body := range []string{`{"unknown":true}`, `{} {}`} {
		request := httptest.NewRequest("PUT", "/", strings.NewReader(body))
		err := decode(httptest.NewRecorder(), request, &api.RelayPolicyConfiguration{})
		assert.Error(t, err)
	}
}

func TestRelayPaginationBoundaries(t *testing.T) {
	for _, query := range []string{"limit=0", "limit=201", "offset=-1", "offset=1000001", "offset=nope"} {
		_, err := pagination(httptest.NewRequest("GET", "/?"+query, nil))
		assert.Error(t, err)
	}
	p, err := pagination(httptest.NewRequest("GET", "/?limit=2&offset=99", nil))
	require.NoError(t, err)
	assert.Empty(t, window([]int{1, 2, 3}, p), "offset beyond end returns a bounded empty page")
}
