package internal

import (
	"errors"
	"fmt"

	nbnetworkmap "github.com/netbirdio/netbird/shared/management/networkmap"
	mgmProto "github.com/netbirdio/netbird/shared/management/proto"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

// decodeComponentNetworkMap runs under syncMsgMux. A rejected transport-policy
// component must revoke the old instructions before the receive loop continues.
func (e *Engine) decodeComponentNetworkMap(envelope *mgmProto.NetworkMapEnvelope, localKey, dnsName string) (*nbnetworkmap.EnvelopeResult, error) {
	result, err := nbnetworkmap.EnvelopeToNetworkMap(e.ctx, envelope, localKey, dnsName, e.config.DisableFirewall)
	if !errors.Is(err, nbnetworkmap.ErrInvalidRelayPolicy) {
		return result, err
	}
	return nil, errors.Join(err, e.revokeInvalidRelayPolicies(envelope.GetFull().GetPairRelayPolicies()))
}

func (e *Engine) revokeInvalidRelayPolicies(incoming map[string]*mgmProto.RelayPolicy) error {
	blocked := &rp.ResolvedRelayPolicy{State: rp.RelayPolicyBlocked, ForceRelay: true}
	e.relayHomeDesired = blocked.Copy()
	e.latestComponents = nil
	var failures []error
	if e.relayManager != nil {
		if err := e.relayManager.SetHomePolicy(blocked); err != nil {
			failures = append(failures, fmt.Errorf("block invalid home relay instructions: %w", err))
		}
	}
	if e.relayPolicyDesired == nil {
		e.relayPolicyDesired = make(map[rp.PeerID]*rp.ResolvedRelayPolicy)
	}
	for key := range e.relayPolicyDesired {
		e.relayPolicyDesired[key] = blocked.Copy()
	}
	for key := range incoming {
		e.relayPolicyDesired[rp.PeerID(key)] = blocked.Copy()
	}
	for _, key := range e.peerStore.PeersPubKey() {
		conn, ok := e.peerStore.PeerConn(key)
		if !ok {
			continue
		}
		_, wasManaged := e.relayPolicyDesired[rp.PeerID(key)]
		if conn.RelayPolicy() == nil && !wasManaged {
			continue
		}
		e.relayPolicyDesired[rp.PeerID(key)] = blocked.Copy()
		if err := e.removePeer(key); err != nil {
			failures = append(failures, fmt.Errorf("revoke invalid relay path: %w", err))
		}
	}
	return errors.Join(failures...)
}
