package relaypolicy

import shared "github.com/netbirdio/netbird/shared/management/relaypolicy"

// ResolveOptions supplies restrictions and targeted retry state owned by the caller.
type ResolveOptions = shared.ResolveOptions

// Validate checks ownership and references without granting connectivity.
func Validate(input shared.RelayPolicyInput) error { return shared.Validate(input) }

// ResolvePair resolves an already ACL-authorized pair symmetrically.
func ResolvePair(input shared.RelayPolicyInput, a, b shared.PeerID, options ResolveOptions) (*shared.ResolvedRelayPolicy, error) {
	return shared.ResolvePair(input, a, b, options)
}

// ResolveHome resolves home choices independently of foreign pair transport.
func ResolveHome(input shared.RelayPolicyInput, peer shared.PeerID, options ResolveOptions) (*shared.ResolvedRelayPolicy, error) {
	return shared.ResolveHome(input, peer, options)
}
