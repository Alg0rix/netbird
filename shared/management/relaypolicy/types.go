package relaypolicy

import "slices"

// Capability is the append-only Management peer capability for relay policy enforcement.
const Capability int32 = 4

type (
	// AccountID identifies an account at the relay-policy boundary.
	AccountID string
	// PeerID identifies a peer at the relay-policy boundary.
	PeerID string
	// PeerGroupID identifies a peer selector group, distinct from a relay group.
	PeerGroupID string
	// RelayID identifies an operator-approved relay instance.
	RelayID string
	// RelayGroupID identifies an account-owned set of relays.
	RelayGroupID string
	// RelayPolicyID identifies a symmetric pair rule.
	RelayPolicyID string
	// RelayRuleID identifies a contributing home profile or pair policy.
	RelayRuleID string
	// RelayProfileID identifies a home-relay profile.
	RelayProfileID string
	// RelayEndpoint is a canonical, operator-approved relay URL.
	RelayEndpoint string
	// RelayPolicyDigest identifies the complete effective transport instructions.
	RelayPolicyDigest string
	// RelayPolicyState distinguishes usable choices from an empty restriction.
	RelayPolicyState string
)

const (
	RelayPolicyReady       RelayPolicyState = "ready"
	RelayPolicyBlocked     RelayPolicyState = "blocked"
	RelayPolicyUnsupported RelayPolicyState = "unsupported"
)

// RelayCatalog is an immutable operator publication shared by Management replicas.
type RelayCatalog struct {
	Generation uint64
	Entries    []RelayCatalogEntry
}

// RelayCatalogEntry maps one stable identity to one concrete relay instance.
type RelayCatalogEntry struct {
	ID       RelayID
	Endpoint RelayEndpoint
	// InstanceEndpoint is the address the approved instance returns in its handshake.
	InstanceEndpoint RelayEndpoint
	Aliases          []RelayEndpoint
	Active           bool
	// Legacy entries preserve advertisement but cannot be referenced by account rules.
	Legacy bool
}

// RelayGroup selects relays, rather than peers, within one account.
type RelayGroup struct {
	ID        RelayGroupID `gorm:"primaryKey"`
	AccountID AccountID    `gorm:"index"`
	Name      string
	Revision  uint64
	RelayIDs  []RelayID `gorm:"serializer:json"`
}

// RelaySelector selects peers directly or through peer-group membership.
type RelaySelector struct {
	PeerIDs  []PeerID
	GroupIDs []PeerGroupID
}

// RelaySelection is a union of relay and relay-group references.
type RelaySelection struct {
	RelayIDs []RelayID
	GroupIDs []RelayGroupID
}

// RelayConstraints narrows transport choices independently of traffic ACLs.
type RelayConstraints struct {
	// Nil Allow means all active approved relays; an empty selection is invalid.
	Allow       *RelaySelection
	Exclude     RelaySelection
	Preferences []RelaySelection
	ForceRelay  bool
}

// RelayHomeProfile restricts a peer's home registration without limiting foreign relays.
type RelayHomeProfile struct {
	ID          RelayProfileID `gorm:"primaryKey"`
	AccountID   AccountID      `gorm:"index"`
	Name        string
	Revision    uint64
	Priority    uint32
	Enabled     bool
	Selector    RelaySelector    `gorm:"serializer:json"`
	Constraints RelayConstraints `gorm:"serializer:json"`
}

// RelayPairPolicy applies equally to both orientations of its selector pair.
type RelayPairPolicy struct {
	ID          RelayPolicyID `gorm:"primaryKey"`
	AccountID   AccountID     `gorm:"index"`
	Name        string
	Revision    uint64
	Priority    uint32
	Enabled     bool
	Source      RelaySelector    `gorm:"serializer:json"`
	Destination RelaySelector    `gorm:"serializer:json"`
	Constraints RelayConstraints `gorm:"serializer:json"`
}

// RelayPeer supplies account membership and selectors without granting connectivity.
type RelayPeer struct {
	ID        PeerID
	AccountID AccountID
	GroupIDs  []PeerGroupID
}

// RelayPolicyInput is an account-scoped snapshot for deterministic resolution.
type RelayPolicyInput struct {
	AccountID AccountID
	Catalog   RelayCatalog
	Groups    []RelayGroup
	Profiles  []RelayHomeProfile
	Policies  []RelayPairPolicy
	Peers     []RelayPeer
	// PeerGroups includes empty groups so their references remain valid.
	PeerGroups []PeerGroupID
	Revision   uint64
}

// RelayPolicyContribution records a matching rule and the relays it eliminates.
type RelayPolicyContribution struct {
	ID         RelayRuleID
	Revision   uint64
	Eliminated []RelayID
}

// ResolvedRelayPolicy is present even when restrictions leave no permitted relay.
// Its absence, represented by a nil pointer, preserves legacy transport behavior.
type ResolvedRelayPolicy struct {
	State                 RelayPolicyState
	Revision              uint64
	CatalogGeneration     uint64
	ReselectionGeneration uint64
	Digest                RelayPolicyDigest
	ForceRelay            bool
	Tiers                 [][]RelayCatalogEntry
	Contributors          []RelayPolicyContribution
	PreferenceRule        RelayRuleID
	Eliminated            []RelayID
}

// Copy returns detached effective instructions, preserving absence.
func (p *ResolvedRelayPolicy) Copy() *ResolvedRelayPolicy {
	if p == nil {
		return nil
	}
	c := *p
	c.Tiers = slices.Clone(p.Tiers)
	for i := range c.Tiers {
		c.Tiers[i] = slices.Clone(c.Tiers[i])
		for j := range c.Tiers[i] {
			c.Tiers[i][j].Aliases = slices.Clone(c.Tiers[i][j].Aliases)
		}
	}
	c.Contributors = slices.Clone(p.Contributors)
	for i := range c.Contributors {
		c.Contributors[i].Eliminated = slices.Clone(c.Contributors[i].Eliminated)
	}
	c.Eliminated = slices.Clone(p.Eliminated)
	return &c
}

// CopyPolicies detaches pair instructions from a shared snapshot.
func CopyPolicies(policies map[PeerID]*ResolvedRelayPolicy) map[PeerID]*ResolvedRelayPolicy {
	if policies == nil {
		return nil
	}
	result := make(map[PeerID]*ResolvedRelayPolicy, len(policies))
	for id, policy := range policies {
		result[id] = policy.Copy()
	}
	return result
}

// Copy returns a deep copy of the group.
func (g RelayGroup) Copy() *RelayGroup {
	g.RelayIDs = slices.Clone(g.RelayIDs)
	return &g
}

// Copy returns a deep copy of the home profile.
func (p RelayHomeProfile) Copy() *RelayHomeProfile {
	p.Selector = copyRelaySelector(p.Selector)
	p.Constraints = copyRelayConstraints(p.Constraints)
	return &p
}

// Copy returns a deep copy of the pair policy.
func (p RelayPairPolicy) Copy() *RelayPairPolicy {
	p.Source = copyRelaySelector(p.Source)
	p.Destination = copyRelaySelector(p.Destination)
	p.Constraints = copyRelayConstraints(p.Constraints)
	return &p
}

func copyRelaySelector(s RelaySelector) RelaySelector {
	return RelaySelector{PeerIDs: slices.Clone(s.PeerIDs), GroupIDs: slices.Clone(s.GroupIDs)}
}

func copyRelaySelection(s RelaySelection) RelaySelection {
	return RelaySelection{RelayIDs: slices.Clone(s.RelayIDs), GroupIDs: slices.Clone(s.GroupIDs)}
}

func copyRelayConstraints(c RelayConstraints) RelayConstraints {
	if c.Allow != nil {
		s := copyRelaySelection(*c.Allow)
		c.Allow = &s
	}
	c.Exclude = copyRelaySelection(c.Exclude)
	c.Preferences = slices.Clone(c.Preferences)
	for i := range c.Preferences {
		c.Preferences[i] = copyRelaySelection(c.Preferences[i])
	}
	return c
}
