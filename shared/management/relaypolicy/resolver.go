package relaypolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// ResolveOptions supplies restrictions and targeted retry state owned by the caller.
type ResolveOptions struct {
	ForceRelay            bool
	ReselectionGeneration uint64
}

// Resolver reuses validated immutable account indexes across authorized pairs.
type Resolver struct{ snapshot *snapshot }

// NewResolver detaches and validates account data once for concurrent read-only resolution.
func NewResolver(input Input) (*Resolver, error) {
	config := Configuration{Groups: input.Groups, Profiles: input.Profiles, Policies: input.Policies}.Copy()
	input.Groups, input.Profiles, input.Policies = config.Groups, config.Profiles, config.Policies
	input.PeerGroups = slices.Clone(input.PeerGroups)
	input.Peers = slices.Clone(input.Peers)
	for i := range input.Peers {
		input.Peers[i].GroupIDs = slices.Clone(input.Peers[i].GroupIDs)
	}
	s, err := newSnapshot(input)
	if err != nil {
		return nil, err
	}
	return &Resolver{snapshot: s}, nil
}

// ResolvePair resolves an already ACL-authorized unordered pair.
func (r *Resolver) ResolvePair(a, b PeerID, options ResolveOptions) (*ResolvedRelayPolicy, error) {
	return r.snapshot.resolvePair(a, b, options)
}

// ResolveHome resolves home registration independently of pair restrictions.
func (r *Resolver) ResolveHome(peer PeerID, options ResolveOptions) (*ResolvedRelayPolicy, error) {
	return r.snapshot.resolveHome(peer, options)
}

type snapshot struct {
	input   Input
	catalog map[RelayID]RelayCatalogEntry
	groups  map[RelayGroupID]RelayGroup
	peers   map[PeerID]RelayPeer
}

type rule struct {
	id          RelayRuleID
	revision    uint64
	priority    uint32
	constraints RelayConstraints
}

type relaySet map[RelayID]struct{}

// Validate checks ownership and all references without activating policies or granting access.
// Retired relay references remain valid so runtime resolution can report a blocked set.
func Validate(input Input) error {
	_, err := newSnapshot(input)
	return err
}

func newSnapshot(input Input) (*snapshot, error) {
	if input.AccountID == "" {
		return nil, fmt.Errorf("missing relay policy account")
	}
	catalog, err := ValidateCatalog(input.Catalog)
	if err != nil {
		return nil, err
	}
	input.Catalog = catalog
	s := &snapshot{input: input, catalog: make(map[RelayID]RelayCatalogEntry),
		groups: make(map[RelayGroupID]RelayGroup), peers: make(map[PeerID]RelayPeer)}
	for _, entry := range catalog.Entries {
		s.catalog[entry.ID] = entry
	}
	for _, group := range input.Groups {
		if group.ID == "" || group.AccountID != input.AccountID {
			return nil, fmt.Errorf("invalid relay group account or identity")
		}
		if _, exists := s.groups[group.ID]; exists {
			return nil, fmt.Errorf("duplicate relay group %s", group.ID)
		}
		for _, id := range group.RelayIDs {
			if err := s.validateRelay(id); err != nil {
				return nil, err
			}
		}
		s.groups[group.ID] = group
	}
	for _, peer := range input.Peers {
		if peer.ID == "" || peer.AccountID != input.AccountID {
			return nil, fmt.Errorf("invalid relay peer account or identity")
		}
		if _, exists := s.peers[peer.ID]; exists {
			return nil, fmt.Errorf("duplicate relay peer %s", peer.ID)
		}
		for _, id := range peer.GroupIDs {
			if !slices.Contains(input.PeerGroups, id) {
				return nil, fmt.Errorf("unknown peer group %s", id)
			}
		}
		s.peers[peer.ID] = peer
	}
	if err := s.validateRules(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *snapshot) validateRules() error {
	policyIDs := make(map[PolicyID]struct{})
	for _, p := range s.input.Policies {
		if p.ID == "" || p.AccountID != s.input.AccountID {
			return fmt.Errorf("invalid pair policy account or identity")
		}
		if _, exists := policyIDs[p.ID]; exists {
			return fmt.Errorf("duplicate relay policy %s", p.ID)
		}
		policyIDs[p.ID] = struct{}{}
		if err := s.validateSelector(p.Source); err != nil {
			return err
		}
		if err := s.validateSelector(p.Destination); err != nil {
			return err
		}
		if len(p.Source.GroupIDs) == 0 && len(p.Destination.GroupIDs) == 0 &&
			len(p.Source.PeerIDs) == 1 && len(p.Destination.PeerIDs) == 1 &&
			p.Source.PeerIDs[0] == p.Destination.PeerIDs[0] {
			return fmt.Errorf("relay policy cannot target a peer paired with itself")
		}
		if err := s.validateConstraints(p.Constraints); err != nil {
			return fmt.Errorf("relay policy %s: %w", p.ID, err)
		}
	}
	profileIDs := make(map[RelayProfileID]struct{})
	for _, p := range s.input.Profiles {
		if p.ID == "" || p.AccountID != s.input.AccountID {
			return fmt.Errorf("invalid home profile account or identity")
		}
		if _, exists := profileIDs[p.ID]; exists {
			return fmt.Errorf("duplicate relay home profile %s", p.ID)
		}
		profileIDs[p.ID] = struct{}{}
		if err := s.validateSelector(p.Selector); err != nil {
			return err
		}
		if p.Constraints.ForceRelay {
			return fmt.Errorf("home profiles cannot force pair transport")
		}
		if err := s.validateConstraints(p.Constraints); err != nil {
			return fmt.Errorf("relay home profile %s: %w", p.ID, err)
		}
	}
	return nil
}

func (s *snapshot) validateSelector(selector RelaySelector) error {
	if len(selector.PeerIDs)+len(selector.GroupIDs) == 0 {
		return fmt.Errorf("empty peer selector")
	}
	for _, id := range selector.PeerIDs {
		if _, exists := s.peers[id]; !exists {
			return fmt.Errorf("unknown account peer %s", id)
		}
	}
	for _, id := range selector.GroupIDs {
		if id == "" || !slices.Contains(s.input.PeerGroups, id) {
			return fmt.Errorf("unknown account peer group %s", id)
		}
	}
	return nil
}

func (s *snapshot) validateRelay(id RelayID) error {
	entry, exists := s.catalog[id]
	if !exists || entry.Legacy {
		return fmt.Errorf("relay %s is not explicitly approved", id)
	}
	return nil
}

func (s *snapshot) validateSelection(selection RelaySelection, requireNonempty bool) error {
	if requireNonempty && len(selection.RelayIDs)+len(selection.GroupIDs) == 0 {
		return fmt.Errorf("explicit relay selection is empty")
	}
	for _, id := range selection.RelayIDs {
		if err := s.validateRelay(id); err != nil {
			return err
		}
	}
	for _, id := range selection.GroupIDs {
		if _, exists := s.groups[id]; !exists {
			return fmt.Errorf("unknown account relay group %s", id)
		}
	}
	return nil
}

func (s *snapshot) validateConstraints(c RelayConstraints) error {
	if c.Allow != nil {
		if err := s.validateSelection(*c.Allow, true); err != nil {
			return err
		}
	}
	if err := s.validateSelection(c.Exclude, false); err != nil {
		return err
	}
	for _, tier := range c.Preferences {
		if err := s.validateSelection(tier, true); err != nil {
			return err
		}
	}
	return nil
}

// ResolvePair resolves an already ACL-authorized pair symmetrically.
// A nil result preserves legacy behavior; callers must never use resolution to grant access.
func ResolvePair(input Input, a, b PeerID, options ResolveOptions) (*ResolvedRelayPolicy, error) {
	s, err := newSnapshot(input)
	if err != nil {
		return nil, err
	}
	return s.resolvePair(a, b, options)
}

func (s *snapshot) resolvePair(a, b PeerID, options ResolveOptions) (*ResolvedRelayPolicy, error) {
	if a == b {
		return nil, fmt.Errorf("cannot resolve a peer paired with itself")
	}
	pa, aExists := s.peers[a]
	pb, bExists := s.peers[b]
	if !aExists || !bExists {
		return nil, fmt.Errorf("pair contains an unknown account peer")
	}
	var rules []rule
	for _, p := range s.input.Policies {
		if !p.Enabled {
			continue
		}
		if (matches(p.Source, pa) && matches(p.Destination, pb)) ||
			(matches(p.Source, pb) && matches(p.Destination, pa)) {
			rules = append(rules, rule{RelayRuleID(p.ID), p.Revision, p.Priority, p.Constraints})
		}
	}
	if a > b {
		a, b = b, a
	}
	return s.resolve(rules, []PeerID{a, b}, options)
}

// ResolveHome resolves only home registration choices, leaving foreign pair transport unchanged.
func ResolveHome(input Input, peerID PeerID, options ResolveOptions) (*ResolvedRelayPolicy, error) {
	s, err := newSnapshot(input)
	if err != nil {
		return nil, err
	}
	return s.resolveHome(peerID, options)
}

func (s *snapshot) resolveHome(peerID PeerID, options ResolveOptions) (*ResolvedRelayPolicy, error) {
	peer, exists := s.peers[peerID]
	if !exists {
		return nil, fmt.Errorf("unknown account peer %s", peerID)
	}
	var rules []rule
	for _, p := range s.input.Profiles {
		if p.Enabled && matches(p.Selector, peer) {
			rules = append(rules, rule{RelayRuleID(p.ID), p.Revision, p.Priority, p.Constraints})
		}
	}
	options.ForceRelay = false
	return s.resolve(rules, []PeerID{peerID}, options)
}

func matches(selector RelaySelector, peer RelayPeer) bool {
	if slices.Contains(selector.PeerIDs, peer.ID) {
		return true
	}
	for _, id := range selector.GroupIDs {
		if slices.Contains(peer.GroupIDs, id) {
			return true
		}
	}
	return false
}

func (s *snapshot) expand(selection RelaySelection) relaySet {
	result := make(relaySet)
	for _, id := range selection.RelayIDs {
		result[id] = struct{}{}
	}
	for _, groupID := range selection.GroupIDs {
		for _, id := range s.groups[groupID].RelayIDs {
			result[id] = struct{}{}
		}
	}
	return result
}

func (s *snapshot) active() relaySet {
	set := make(relaySet)
	for id, entry := range s.catalog {
		if entry.Active && !entry.Legacy {
			set[id] = struct{}{}
		}
	}
	return set
}

func sortedIDs(set relaySet) []RelayID {
	ids := make([]RelayID, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (s *snapshot) resolve(rules []rule, peers []PeerID, options ResolveOptions) (*ResolvedRelayPolicy, error) {
	if len(rules) == 0 {
		return nil, nil //nolint:nilnil // Absence distinguishes unmanaged peers from blocked policies.
	}
	slices.SortFunc(rules, func(a, b rule) int {
		if a.priority < b.priority {
			return -1
		}
		if a.priority > b.priority {
			return 1
		}
		return strings.Compare(string(a.id), string(b.id))
	})
	result := &ResolvedRelayPolicy{State: RelayPolicyReady,
		Revision: s.input.Revision, CatalogGeneration: s.input.Catalog.Generation,
		ReselectionGeneration: options.ReselectionGeneration, ForceRelay: options.ForceRelay}
	permitted := s.active()
	eliminated := make(relaySet)
	for _, r := range rules {
		allowed := s.active()
		if r.constraints.Allow != nil {
			allowed = s.expand(*r.constraints.Allow)
		}
		denied := s.expand(r.constraints.Exclude)
		removed := make(relaySet)
		// Explain each contributor independently so an earlier denial cannot hide a later one.
		for id := range s.active() {
			_, included := allowed[id]
			_, excluded := denied[id]
			if !included || excluded {
				delete(permitted, id)
				removed[id] = struct{}{}
				eliminated[id] = struct{}{}
			}
		}
		result.Contributors = append(result.Contributors, Contribution{
			ID: r.id, Revision: r.revision, Eliminated: sortedIDs(removed),
		})
		result.ForceRelay = result.ForceRelay || r.constraints.ForceRelay
	}
	result.Eliminated = sortedIDs(eliminated)
	if len(permitted) == 0 {
		result.State = RelayPolicyBlocked
	}
	for _, r := range rules {
		if len(r.constraints.Preferences) == 0 {
			continue
		}
		result.PreferenceRule = r.id
		for _, tier := range r.constraints.Preferences {
			result.Tiers = s.appendTier(result.Tiers, s.expand(tier), permitted)
		}
		break
	}
	result.Tiers = s.appendTier(result.Tiers, permitted, permitted)
	// Account edits still need acknowledgment but must not reconnect unrelated pairs.
	digestPolicy := *result
	digestPolicy.Revision = 0
	payload, err := json.Marshal(struct {
		AccountID AccountID
		Peers     []PeerID
		Policy    *ResolvedRelayPolicy
	}{s.input.AccountID, peers, &digestPolicy})
	if err != nil {
		return nil, fmt.Errorf("encode resolved relay policy: %w", err)
	}
	digest := sha256.Sum256(payload)
	result.Digest = Digest(hex.EncodeToString(digest[:]))
	return result, nil
}

func (s *snapshot) appendTier(tiers [][]RelayCatalogEntry, selected, remaining relaySet) [][]RelayCatalogEntry {
	var tier []RelayCatalogEntry
	for _, id := range sortedIDs(selected) {
		if _, allowed := remaining[id]; !allowed {
			continue
		}
		entry := s.catalog[id]
		entry.Aliases = slices.Clone(entry.Aliases)
		tier = append(tier, entry)
		delete(remaining, id)
	}
	if len(tier) > 0 {
		return append(tiers, tier)
	}
	return tiers
}
