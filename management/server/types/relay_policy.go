package types

import "github.com/netbirdio/netbird/shared/management/relaypolicy"

// AccountID is the shared relay-policy domain type.
type AccountID = relaypolicy.AccountID

// PeerID is the shared relay-policy domain type.
type PeerID = relaypolicy.PeerID

// PeerGroupID is the shared relay-policy domain type.
type PeerGroupID = relaypolicy.PeerGroupID

// RelayID is the shared relay-policy domain type.
type RelayID = relaypolicy.RelayID

// RelayGroupID is the shared relay-policy domain type.
type RelayGroupID = relaypolicy.RelayGroupID

// RelayPolicyID is the shared relay-policy domain type.
type RelayPolicyID = relaypolicy.RelayPolicyID

// RelayRuleID is the shared relay-policy domain type.
type RelayRuleID = relaypolicy.RelayRuleID

// RelayProfileID is the shared relay-policy domain type.
type RelayProfileID = relaypolicy.RelayProfileID

// RelayEndpoint is the shared relay-policy domain type.
type RelayEndpoint = relaypolicy.RelayEndpoint

// RelayPolicyDigest is the shared relay-policy domain type.
type RelayPolicyDigest = relaypolicy.RelayPolicyDigest

// RelayPolicyState is the shared relay-policy domain type.
type RelayPolicyState = relaypolicy.RelayPolicyState

// RelayCatalog is the shared relay-policy domain type.
type RelayCatalog = relaypolicy.RelayCatalog

// RelayCatalogEntry is the shared relay-policy domain type.
type RelayCatalogEntry = relaypolicy.RelayCatalogEntry

// RelayGroup is the shared relay-policy domain type.
type RelayGroup = relaypolicy.RelayGroup

// RelaySelector is the shared relay-policy domain type.
type RelaySelector = relaypolicy.RelaySelector

// RelaySelection is the shared relay-policy domain type.
type RelaySelection = relaypolicy.RelaySelection

// RelayConstraints is the shared relay-policy domain type.
type RelayConstraints = relaypolicy.RelayConstraints

// RelayHomeProfile is the shared relay-policy domain type.
type RelayHomeProfile = relaypolicy.RelayHomeProfile

// RelayPairPolicy is the shared relay-policy domain type.
type RelayPairPolicy = relaypolicy.RelayPairPolicy

// RelayPeer is the shared relay-policy domain type.
type RelayPeer = relaypolicy.RelayPeer

// RelayPolicyInput is the shared relay-policy domain type.
type RelayPolicyInput = relaypolicy.RelayPolicyInput

// RelayPolicyContribution is the shared relay-policy domain type.
type RelayPolicyContribution = relaypolicy.RelayPolicyContribution

// ResolvedRelayPolicy is the shared relay-policy domain type.
type ResolvedRelayPolicy = relaypolicy.ResolvedRelayPolicy

const (
	RelayPolicyReady   = relaypolicy.RelayPolicyReady
	RelayPolicyBlocked = relaypolicy.RelayPolicyBlocked
)

// RelayAccountState is the account's persisted relay configuration revision.
type RelayAccountState = relaypolicy.RelayAccountState

// RelayReselection is a persisted targeted relay retry directive.
type RelayReselection = relaypolicy.RelayReselection
