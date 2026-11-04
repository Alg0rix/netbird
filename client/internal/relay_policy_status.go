package internal

import (
	"context"
	"sort"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/netbirdio/netbird/client/internal/peer"
	"github.com/netbirdio/netbird/shared/management/networkmap"
	mgmProto "github.com/netbirdio/netbird/shared/management/proto"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

type relayPolicyReporter interface {
	ReportRelayPolicyStatus(context.Context, []*mgmProto.RelayPathReport) error
}

func (e *Engine) captureRelayPolicyIntent(nm *mgmProto.NetworkMap) {
	desired := make(map[rp.PeerID]*rp.ResolvedRelayPolicy)
	for _, remote := range nm.GetRemotePeers() {
		if remote.GetRelayPolicy() == nil {
			continue
		}
		p, err := networkmap.FromProtocolRelayPolicy(remote.GetRelayPolicy())
		if err != nil {
			p = &rp.ResolvedRelayPolicy{State: rp.RelayPolicyBlocked, ForceRelay: true}
		}
		desired[rp.PeerID(remote.GetWgPubKey())] = p.Copy()
	}
	e.relayPolicyDesired = desired
	if config := nm.GetPeerConfig(); config != nil {
		p, err := networkmap.FromProtocolRelayPolicy(config.GetHomeRelayPolicy())
		if err != nil {
			p = &rp.ResolvedRelayPolicy{State: rp.RelayPolicyBlocked, ForceRelay: true}
		}
		e.relayHomeDesired = p.Copy()
	}
}

func (e *Engine) relayPolicyObservations() []peer.RelayPolicyObservation {
	e.syncMsgMux.Lock()
	defer e.syncMsgMux.Unlock()
	observations := make([]peer.RelayPolicyObservation, 0, len(e.relayPolicyDesired)+1)
	for key, desired := range e.relayPolicyDesired {
		o := peer.NewRelayPolicyObservation(string(key), desired)
		if conn, ok := e.peerStore.PeerConn(string(key)); ok {
			current := conn.RelayPolicyObservation()
			if current.Digest == desired.Digest && current.CatalogGeneration == desired.CatalogGeneration && current.ReselectionGeneration == desired.ReselectionGeneration && current.DesiredRevision == desired.Revision {
				o = current
			} else {
				o.Reason = rp.ReasonPolicyMismatch
				o.AppliedRevision = current.AppliedRevision
			}
		}
		observations = append(observations, o)
	}
	if desired := e.relayHomeDesired; desired != nil {
		o := peer.NewRelayPolicyObservation("", desired)
		if desired.State == rp.RelayPolicyBlocked && desired.Digest != "" {
			o.State = rp.StateBlocked
			o.Reason = rp.ReasonNoPermittedRelay
		}
		if e.relayManager != nil {
			applied, id, reason := e.relayManager.HomePolicyStatus()
			if applied != nil && applied.Digest == desired.Digest && applied.CatalogGeneration == desired.CatalogGeneration && applied.ReselectionGeneration == desired.ReselectionGeneration {
				if desired.State != rp.RelayPolicyUnsupported {
					o.AppliedRevision = applied.Revision
				}
				if o.State == rp.StatePending {
					o.Reason = reason
				}
				if id != "" && applied.Revision == desired.Revision {
					o.RelayID = id
					o.State = rp.StateApplied
					o.Reason = rp.ReasonNone
					o.Transport = rp.TransportNativeRelay
					for i, tier := range desired.Tiers {
						for _, entry := range tier {
							if entry.ID == id && i > 0 {
								o.Reason = rp.ReasonHigherPriorityUnavailable
							}
						}
					}
				}
			}
		}
		observations = append(observations, o)
	}
	sort.Slice(observations, func(i, j int) bool { return observations[i].RemotePeerKey < observations[j].RemotePeerKey })
	return observations
}

func reportFromObservation(o peer.RelayPolicyObservation) *mgmProto.RelayPathReport {
	transport := mgmProto.RelayPathTransport_RELAY_PATH_TRANSPORT_UNKNOWN
	switch o.Transport {
	case rp.TransportDirect:
		transport = mgmProto.RelayPathTransport_RELAY_PATH_TRANSPORT_DIRECT
	case rp.TransportNativeRelay:
		transport = mgmProto.RelayPathTransport_RELAY_PATH_TRANSPORT_NATIVE_RELAY
	}
	state := mgmProto.RelayPathState_RELAY_PATH_STATE_PENDING
	switch o.State {
	case rp.StateApplied:
		state = mgmProto.RelayPathState_RELAY_PATH_STATE_APPLIED
	case rp.StateBlocked:
		state = mgmProto.RelayPathState_RELAY_PATH_STATE_BLOCKED
	case rp.StateUnsupported:
		state = mgmProto.RelayPathState_RELAY_PATH_STATE_UNSUPPORTED
	}
	return &mgmProto.RelayPathReport{RemotePeerKey: string(o.RemotePeerKey), Revision: o.DesiredRevision, Digest: string(o.Digest), CatalogGeneration: o.CatalogGeneration, ReselectionGeneration: o.ReselectionGeneration, Attempt: o.Attempt, Transport: transport, RelayId: string(o.RelayID), State: state, Reason: string(o.Reason)}
}

func (e *Engine) startRelayPolicyReporter(ctx context.Context) {
	e.shutdownWg.Add(1)
	go func() {
		defer e.shutdownWg.Done()
		defer e.statusRecorder.SetRelayPolicyObservations(nil)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var lastReport time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			observations := e.relayPolicyObservations()
			e.statusRecorder.SetRelayPolicyObservations(observations)
			reporter, ok := e.mgmClient.(relayPolicyReporter)
			if !ok || time.Since(lastReport) < rp.ReportInterval {
				continue
			}
			lastReport = time.Now()
			reports := make([]*mgmProto.RelayPathReport, 0, len(observations))
			for _, observation := range observations {
				if observation.Digest != "" {
					reports = append(reports, reportFromObservation(observation))
				}
			}
			for offset := 0; offset < len(reports); offset += rp.MaxReports {
				end := min(offset+rp.MaxReports, len(reports))
				if err := reporter.ReportRelayPolicyStatus(ctx, reports[offset:end]); err != nil {
					log.Debugf("report relay policy observations: %v", err)
					break
				}
			}
		}
	}()
}
