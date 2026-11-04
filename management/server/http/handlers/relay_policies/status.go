package relaypolicies

import (
	"net/http"

	"github.com/netbirdio/netbird/shared/management/http/api"
	"github.com/netbirdio/netbird/shared/management/http/util"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
)

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	accountID, userID, err := scope(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	state, err := h.manager.Status(r.Context(), accountID, userID, rp.PeerID(r.URL.Query().Get("peer_a")), rp.PeerID(r.URL.Query().Get("peer_b")))
	if err != nil {
		writeError(w, r, err)
		return
	}
	response := api.RelayPolicyStatus{PeerA: string(state.PeerA), PeerB: string(state.PeerB), Policy: effective(state.Policy),
		Application: api.RelayPolicyStatusApplication(state.Application), Healthy: state.Healthy,
		Transport: api.RelayPolicyStatusTransport(state.Transport), RelayId: string(state.RelayID),
		Endpoints: make([]api.RelayPathObservation, len(state.Endpoints))}
	for i, e := range state.Endpoints {
		response.Endpoints[i] = api.RelayPathObservation{LocalPeerId: string(e.LocalPeerID), RemotePeerId: string(e.Report.RemotePeerID),
			Revision: e.Report.Revision, Digest: string(e.Report.Digest), CatalogGeneration: e.Report.CatalogGeneration,
			ReselectionGeneration: e.Report.ReselectionGeneration, Sequence: e.Report.Sequence, Attempt: e.Report.Attempt,
			Transport: api.RelayPathObservationTransport(e.Report.Transport), RelayId: string(e.Report.RelayID), State: api.RelayPathObservationState(e.Report.State),
			Reason: api.RelayPathObservationReason(e.Report.Reason), ObservedAt: e.ObservedAt, ExpiresAt: e.ExpiresAt, Current: e.Current}
	}
	util.WriteJSONObject(r.Context(), w, response)
}
