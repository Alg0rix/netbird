package relaypolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/netbirdio/netbird/management/server/types"
)

// ResolveOptions supplies restrictions and targeted retry state owned by the caller.
type ResolveOptions struct {
	ForceRelay            bool
	ReselectionGeneration uint64
}

type snapshot struct {
	input   types.RelayPolicyInput
	catalog map[types.RelayID]types.RelayCatalogEntry
	groups  map[types.RelayGroupID]types.RelayGroup
	peers   map[types.PeerID]types.RelayPeer
}

type rule struct {
	id          types.RelayRuleID
	revision    uint64
	priority    uint32
	constraints types.RelayConstraints
}

type relaySet map[types.RelayID]struct{}

// Validate checks ownership and all references without activating policies or granting access.
// Retired relay references remain valid so runtime resolution can report a blocked set.
func Validate(input types.RelayPolicyInput) error {
	_, err := newSnapshot(input)
	return err
}

func newSnapshot(input types.RelayPolicyInput) (*snapshot, error) {
	if input.AccountID == "" {
		return nil, fmt.Errorf("missing relay policy account")
	}
	catalog, err := ValidateCatalog(input.Catalog)
	if err != nil {
		return nil, err
	}
	input.Catalog = catalog
	s := &snapshot{input: input, catalog: make(map[types.RelayID]types.RelayCatalogEntry),
		groups: make(map[types.RelayGroupID]types.RelayGroup), peers: make(map[types.PeerID]types.RelayPeer)}
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
	policyIDs := make(map[types.RelayPolicyID]struct{})
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
	profileIDs := make(map[types.RelayProfileID]struct{})
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

func (s *snapshot) validateSelector(selector types.RelaySelector) error {
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

func (s *snapshot) validateRelay(id types.RelayID) error {
	entry, exists := s.catalog[id]
	if !exists || entry.Legacy {
		return fmt.Errorf("relay %s is not explicitly approved", id)
	}
	return nil
}

func (s *snapshot) validateSelection(selection types.RelaySelection, requireNonempty bool) error {
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

func (s *snapshot) validateConstraints(c types.RelayConstraints) error {
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
func ResolvePair(input types.RelayPolicyInput, a, b types.PeerID, options ResolveOptions) (*types.ResolvedRelayPolicy, error) {
	s, err := newSnapshot(input)
	if err != nil {
		return nil, err
	}
	if a == b {
		return nil, fmt.Errorf("cannot resolve a peer paired with itself")
	}
	pa, aExists := s.peers[a]
	pb, bExists := s.peers[b]
	if !aExists || !bExists {
		return nil, fmt.Errorf("pair contains an unknown account peer")
	}
	var rules []rule
	for _, p := range input.Policies {
		if !p.Enabled {
			continue
		}
		if (matches(p.Source, pa) && matches(p.Destination, pb)) ||
			(matches(p.Source, pb) && matches(p.Destination, pa)) {
			rules = append(rules, rule{types.RelayRuleID(p.ID), p.Revision, p.Priority, p.Constraints})
		}
	}
	if a > b {
		a, b = b, a
	}
	return s.resolve(rules, []types.PeerID{a, b}, options)
}

// ResolveHome resolves only home registration choices, leaving foreign pair transport unchanged.
func ResolveHome(input types.RelayPolicyInput, peerID types.PeerID, options ResolveOptions) (*types.ResolvedRelayPolicy, error) {
	s, err := newSnapshot(input)
	if err != nil {
		return nil, err
	}
	peer, exists := s.peers[peerID]
	if !exists {
		return nil, fmt.Errorf("unknown account peer %s", peerID)
	}
	var rules []rule
	for _, p := range input.Profiles {
		if p.Enabled && matches(p.Selector, peer) {
			rules = append(rules, rule{types.RelayRuleID(p.ID), p.Revision, p.Priority, p.Constraints})
		}
	}
	options.ForceRelay = false
	return s.resolve(rules, []types.PeerID{peerID}, options)
}

func matches(selector types.RelaySelector, peer types.RelayPeer) bool {
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

func (s *snapshot) expand(selection types.RelaySelection) relaySet {
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

func sortedIDs(set relaySet) []types.RelayID {
	ids := make([]types.RelayID, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (s *snapshot) resolve(rules []rule, peers []types.PeerID, options ResolveOptions) (*types.ResolvedRelayPolicy, error) {
	if len(rules) == 0 {
		return nil, nil
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
	result := &types.ResolvedRelayPolicy{State: types.RelayPolicyReady,
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
		result.Contributors = append(result.Contributors, types.RelayPolicyContribution{
			ID: r.id, Revision: r.revision, Eliminated: sortedIDs(removed),
		})
		result.ForceRelay = result.ForceRelay || r.constraints.ForceRelay
	}
	result.Eliminated = sortedIDs(eliminated)
	if len(permitted) == 0 {
		result.State = types.RelayPolicyBlocked
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
		AccountID types.AccountID
		Peers     []types.PeerID
		Policy    *types.ResolvedRelayPolicy
	}{s.input.AccountID, peers, &digestPolicy})
	if err != nil {
		return nil, fmt.Errorf("encode resolved relay policy: %w", err)
	}
	digest := sha256.Sum256(payload)
	result.Digest = types.RelayPolicyDigest(hex.EncodeToString(digest[:]))
	return result, nil
}

func (s *snapshot) appendTier(tiers [][]types.RelayCatalogEntry, selected, remaining relaySet) [][]types.RelayCatalogEntry {
	var tier []types.RelayCatalogEntry
	for _, id := range sortedIDs(selected) {
		if _, allowed := remaining[id]; !allowed {
			continue
		}
		tier = append(tier, s.catalog[id])
		delete(remaining, id)
	}
	if len(tier) > 0 {
		return append(tiers, tier)
	}
	return tiers
}
