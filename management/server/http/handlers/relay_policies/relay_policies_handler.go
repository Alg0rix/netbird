package relaypolicies

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/netbirdio/netbird/management/server/account"
	nbcontext "github.com/netbirdio/netbird/management/server/context"
	"github.com/netbirdio/netbird/management/server/permissions"
	"github.com/netbirdio/netbird/management/server/relaypolicy/admin"
	"github.com/netbirdio/netbird/management/server/store"
	"github.com/netbirdio/netbird/management/server/types"
	"github.com/netbirdio/netbird/shared/management/http/api"
	"github.com/netbirdio/netbird/shared/management/http/util"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
	"github.com/netbirdio/netbird/shared/management/status"
)

const maxRequestBytes = 1 << 20

type handler struct {
	manager *admin.Manager
	store   store.Store
}

// AddEndpoints registers account-scoped relay administration through the authorized manager.
func AddEndpoints(accounts account.Manager, permissions permissions.Manager, router *mux.Router) {
	h := &handler{store: accounts.GetStore(), manager: admin.NewManager(accounts.GetStore(), permissions, admin.Hooks{
		Audit: accounts.StoreEvent,
		Update: func(ctx context.Context, accountID string) {
			accounts.UpdateAccountPeers(ctx, accountID, types.UpdateReason{Resource: types.UpdateResourcePolicy, Operation: types.UpdateOperationUpdate})
		},
	})}
	h.registerEndpoints(router)
}

func (h *handler) registerEndpoints(router *mux.Router) {
	router.HandleFunc("/relay-catalog", h.catalog).Methods(http.MethodGet, http.MethodOptions)
	router.HandleFunc("/relay-policies/configuration", h.configuration).Methods(http.MethodGet, http.MethodPut, http.MethodOptions)
	router.HandleFunc("/relay-policies/preview", h.preview).Methods(http.MethodPost, http.MethodOptions)
	router.HandleFunc("/relay-policies/effective", h.effective).Methods(http.MethodGet, http.MethodOptions)
	router.HandleFunc("/relay-policies/status", h.status).Methods(http.MethodGet, http.MethodOptions)
	router.HandleFunc("/relay-policies/retry-preferred", h.retry).Methods(http.MethodPost, http.MethodOptions)
	for path, kind := range map[string]resourceKind{"/relay-groups": groupKind, "/relay-home-profiles": profileKind, "/relay-pair-policies": pairKind} {
		router.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) { h.resource(w, r, kind) }).Methods(http.MethodGet, http.MethodPost, http.MethodOptions)
		router.HandleFunc(path+"/{resourceId}", func(w http.ResponseWriter, r *http.Request) { h.resource(w, r, kind) }).Methods(http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodOptions)
	}
}

func scope(r *http.Request) (rp.AccountID, rp.UserID, error) {
	auth, err := nbcontext.GetUserAuthFromContext(r.Context())
	if err != nil {
		return "", "", status.Errorf(status.Unauthenticated, "user authentication is required")
	}
	return rp.AccountID(auth.AccountId), rp.UserID(auth.UserId), nil
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrRelayRevisionConflict) {
		util.WriteErrorResponse("relay configuration changed; reload and preview before retrying", http.StatusConflict, w)
		return
	}
	util.WriteError(r.Context(), err, w)
}

func decode(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return status.Errorf(status.InvalidArgument, "invalid relay request JSON")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return status.Errorf(status.InvalidArgument, "request must contain one JSON object")
	}
	return nil
}

func (h *handler) catalog(w http.ResponseWriter, r *http.Request) {
	accountID, userID, err := scope(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	snapshot, err := h.manager.Get(r.Context(), accountID, userID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	response := api.RelayCatalogResponse{Generation: snapshot.Catalog.Generation, Entries: make([]api.RelayCatalogItem, len(snapshot.Catalog.Entries))}
	for i, entry := range snapshot.Catalog.Entries {
		response.Entries[i] = catalogEntry(entry)
	}
	util.WriteJSONObject(r.Context(), w, response)
}

func (h *handler) configuration(w http.ResponseWriter, r *http.Request) {
	accountID, userID, err := scope(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if r.Method == http.MethodGet {
		snapshot, err := h.manager.Get(r.Context(), accountID, userID)
		if err != nil {
			writeError(w, r, err)
			return
		}
		util.WriteJSONObject(r.Context(), w, configuration(snapshot.Configuration, snapshot.Catalog.Generation))
		return
	}
	var request api.RelayPolicyConfiguration
	if err := decode(w, r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	saved, err := h.manager.Save(r.Context(), accountID, userID, request.Revision, request.CatalogGeneration, fromConfiguration(request, accountID))
	if err != nil {
		writeError(w, r, err)
		return
	}
	util.WriteJSONObject(r.Context(), w, configuration(saved, request.CatalogGeneration))
}

func (h *handler) preview(w http.ResponseWriter, r *http.Request) {
	accountID, userID, err := scope(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request api.RelayPolicyConfiguration
	if err := decode(w, r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	preview, err := h.manager.Preview(r.Context(), accountID, userID, fromConfiguration(request, accountID))
	if err != nil {
		writeError(w, r, err)
		return
	}
	response := api.RelayPolicyPreview{Revision: preview.Revision, CatalogGeneration: preview.CatalogGeneration, Home: make([]api.RelayHomePreview, len(preview.Home)), Pairs: make([]api.RelayPairPreview, len(preview.Pairs)), Truncated: preview.Truncated}
	for i, value := range preview.Home {
		response.Home[i] = api.RelayHomePreview{PeerId: string(value.PeerID), Policy: effective(value.Policy), Unsupported: value.Unsupported}
	}
	for i, value := range preview.Pairs {
		response.Pairs[i] = api.RelayPairPreview{PeerA: string(value.PeerA), PeerB: string(value.PeerB), Policy: effective(value.Policy), Unsupported: value.Unsupported}
	}
	util.WriteJSONObject(r.Context(), w, response)
}

func (h *handler) effective(w http.ResponseWriter, r *http.Request) {
	accountID, userID, err := scope(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	policy, err := h.manager.Effective(r.Context(), accountID, userID, rp.PeerID(r.URL.Query().Get("peer_a")), rp.PeerID(r.URL.Query().Get("peer_b")))
	if err != nil {
		writeError(w, r, err)
		return
	}
	util.WriteJSONObject(r.Context(), w, api.RelayEffectiveResponse{Policy: effective(policy)})
}

func (h *handler) retry(w http.ResponseWriter, r *http.Request) {
	accountID, userID, err := scope(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request api.RelayRetryRequest
	if err := decode(w, r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	var b rp.PeerID
	if request.PeerB != nil {
		b = rp.PeerID(*request.PeerB)
	}
	result, err := h.manager.Retry(r.Context(), accountID, userID, rp.PeerID(request.PeerA), b, rp.Digest(request.ExpectedDigest))
	if err != nil {
		writeError(w, r, err)
		return
	}
	util.WriteJSONObject(r.Context(), w, api.RelayRetryResponse{Generation: result.Generation, Policy: effective(result.Policy)})
}

func requiredRevision(r *http.Request, name string) (uint64, error) {
	value, err := strconv.ParseUint(r.URL.Query().Get(name), 10, 64)
	if err != nil {
		return 0, status.Errorf(status.InvalidArgument, "%s must be an unsigned integer", name)
	}
	return value, nil
}
