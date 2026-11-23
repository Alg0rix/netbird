package networkmap

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/netbirdio/netbird/shared/management/networkmap/nmdata"
	"github.com/netbirdio/netbird/shared/management/proto"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

// The fork keeps its wire numbers in the reserved range 1000+ so an upstream merge never collides.
// Three Go constants must always match the proto enum; a mismatch silently disables relay policy.
func TestForkCapabilityMatchesProtoEnum(t *testing.T) {
	want := int32(proto.PeerCapability_PeerCapabilityRelayPolicy)
	assert.GreaterOrEqual(t, want, int32(1000), "fork enum values must stay in the reserved range")
	assert.Equal(t, want, nmdata.PeerCapabilityRelayPolicy)
	assert.Equal(t, want, rp.Capability)
}
