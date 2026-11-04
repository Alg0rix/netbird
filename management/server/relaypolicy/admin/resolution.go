package admin

import (
	"context"
	"slices"

	nbpeer "github.com/netbirdio/netbird/management/server/peer"
	"github.com/netbirdio/netbird/management/server/relaypolicy"
	"github.com/netbirdio/netbird/management/server/types"
	sharedrelay "github.com/netbirdio/netbird/shared/management/relaypolicy"
	"github.com/netbirdio/netbird/shared/management/status"
)

// ValidateAccount rejects invalid activation against transaction-local peer and group state.
// Read-only previews retain blocked and unsupported results instead of rejecting them.
func ValidateAccount(ctx context.Context, account *types.Account) error {
	_, err := previewAccount(ctx, account, true)
	return err
}

func previewAccount(ctx context.Context, account *types.Account, strict bool) (*Preview, error) {
	input := account.RelayPolicyInput()
	if err := relaypolicy.Validate(input); err != nil {
		return nil, status.Errorf(status.InvalidArgument, "invalid relay configuration: %v", err)
	}
	preview := &Preview{Revision: input.Revision, CatalogGeneration: input.Catalog.Generation}
	peerIDs := make([]string, 0, len(account.Peers))
	for id := range account.Peers {
		peerIDs = append(peerIDs, id)
	}
	slices.Sort(peerIDs)
	validated := validatedPeers(account)
	for _, id := range peerIDs {
		peerID := sharedrelay.PeerID(id)
		home, err := relaypolicy.ResolveHome(input, peerID, relaypolicy.ResolveOptions{ReselectionGeneration: generation(account, peerID, "")})
		if err != nil {
			return nil, err
		}
		if home != nil {
			unsupported := !supports(account, peerID)
			if err := validateActivation(home, unsupported, strict); err != nil {
				return nil, err
			}
			if len(preview.Home) < previewLimit {
				preview.Home = append(preview.Home, HomePreview{PeerID: peerID, Policy: home, Unsupported: unsupported})
			} else {
				preview.Truncated = true
			}
		}
		peers, _, _, _ := account.GetPeerConnectionResources(ctx, account.Peers[id], validated, nil)
		for _, remote := range peers {
			if remote.ID <= id {
				continue
			}
			otherID := sharedrelay.PeerID(remote.ID)
			policy, err := relaypolicy.ResolvePair(input, peerID, otherID, relaypolicy.ResolveOptions{ReselectionGeneration: generation(account, peerID, otherID)})
			if err != nil {
				return nil, err
			}
			if policy == nil {
				continue
			}
			unsupported := !supports(account, peerID) || !supports(account, otherID)
			if err := validateActivation(policy, unsupported, strict); err != nil {
				return nil, err
			}
			if len(preview.Pairs) < previewLimit {
				preview.Pairs = append(preview.Pairs, PairPreview{PeerA: peerID, PeerB: otherID, Policy: policy, Unsupported: unsupported})
			} else {
				preview.Truncated = true
			}
		}
	}
	return preview, nil
}

func validateActivation(policy *sharedrelay.ResolvedRelayPolicy, unsupported, strict bool) error {
	if !strict {
		return nil
	}
	if unsupported {
		return status.Errorf(status.PreconditionFailed, "relay policy requires supporting clients at every assigned endpoint")
	}
	if policy.State == sharedrelay.RelayPolicyBlocked {
		return status.Errorf(status.PreconditionFailed, "relay policy activation leaves no permitted active relay")
	}
	return nil
}

func supports(account *types.Account, id sharedrelay.PeerID) bool {
	peer := account.Peers[string(id)]
	return peer != nil && peer.HasCapability(sharedrelay.Capability)
}

func validatedPeers(account *types.Account) map[string]struct{} {
	validated := make(map[string]struct{}, len(account.Peers))
	for id, peer := range account.Peers {
		if peer.Status != nil && !peer.Status.RequiresApproval && !peer.Status.LoginExpired {
			validated[id] = struct{}{}
		}
	}
	return validated
}

func generation(account *types.Account, a, b sharedrelay.PeerID) uint64 {
	if b != "" && a > b {
		a, b = b, a
	}
	for _, directive := range account.RelayReselections {
		if directive.PeerA == a && directive.PeerB == b {
			return directive.Generation
		}
	}
	return 0
}

func effective(ctx context.Context, account *types.Account, a, b sharedrelay.PeerID) (*sharedrelay.ResolvedRelayPolicy, error) {
	if a == "" || account.Peers[string(a)] == nil || a == b {
		return nil, status.Errorf(status.NotFound, "relay target not found")
	}
	if b != "" {
		if account.Peers[string(b)] == nil {
			return nil, status.Errorf(status.NotFound, "relay target not found")
		}
		peers, _, _, _ := account.GetPeerConnectionResources(ctx, account.Peers[string(a)], validatedPeers(account), nil)
		if !slices.ContainsFunc(peers, func(peer *nbpeer.Peer) bool { return peer.ID == string(b) }) {
			return nil, status.Errorf(status.PermissionDenied, "pair is not authorized by access policy")
		}
	}
	return resolveWithGeneration(account, a, b, generation(account, a, b))
}

func resolveWithGeneration(account *types.Account, a, b sharedrelay.PeerID, attemptGeneration uint64) (*sharedrelay.ResolvedRelayPolicy, error) {
	input := account.RelayPolicyInput()
	options := relaypolicy.ResolveOptions{ReselectionGeneration: attemptGeneration}
	if b == "" {
		return relaypolicy.ResolveHome(input, a, options)
	}
	return relaypolicy.ResolvePair(input, a, b, options)
}
