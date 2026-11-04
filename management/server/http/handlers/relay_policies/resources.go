package relaypolicies

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/rs/xid"

	"github.com/netbirdio/netbird/shared/management/http/api"
	"github.com/netbirdio/netbird/shared/management/http/util"
	rp "github.com/netbirdio/netbird/shared/management/relaypolicy"
	"github.com/netbirdio/netbird/shared/management/status"
)

type resourceKind string

const (
	groupKind   resourceKind = "group"
	profileKind resourceKind = "home_profile"
	pairKind    resourceKind = "pair_policy"
)

func (h *handler) resource(w http.ResponseWriter, r *http.Request, kind resourceKind) {
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
	id := mux.Vars(r)["resourceId"]
	if r.Method == http.MethodGet {
		if id != "" {
			h.readResource(w, r, kind, snapshot.Configuration, id)
			return
		}
		h.listResources(w, r, kind, snapshot, accountID)
		return
	}
	candidate := snapshot.Configuration.Copy()
	var revision, generation uint64
	if r.Method == http.MethodDelete {
		revision, err = requiredRevision(r, "expected_revision")
		if err == nil {
			generation, err = requiredRevision(r, "catalog_generation")
		}
		if err == nil {
			err = deleteResource(&candidate, kind, id)
		}
	} else {
		revision, generation, err = mutateResource(w, r, &candidate, kind, id, rp.AccountID(accountID))
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	saved, err := h.manager.Save(r.Context(), accountID, userID, revision, generation, candidate)
	if err != nil {
		writeError(w, r, err)
		return
	}
	util.WriteJSONObject(r.Context(), w, configuration(saved, generation))
}

func (h *handler) readResource(w http.ResponseWriter, r *http.Request, kind resourceKind, candidate rp.Configuration, id string) {
	var result any
	switch kind {
	case groupKind:
		for _, value := range candidate.Groups {
			if string(value.ID) == id {
				result = group(value)
				break
			}
		}
	case profileKind:
		for _, value := range candidate.Profiles {
			if string(value.ID) == id {
				result = profile(value)
				break
			}
		}
	case pairKind:
		for _, value := range candidate.Policies {
			if string(value.ID) == id {
				result = pairPolicy(value)
				break
			}
		}
	}
	if result == nil {
		writeError(w, r, status.Errorf(status.NotFound, "relay resource not found"))
		return
	}
	util.WriteJSONObject(r.Context(), w, result)
}

func mutationID(r *http.Request, pathID, bodyID string) (string, error) {
	if r.Method == http.MethodPost {
		if bodyID != "" {
			return "", status.Errorf(status.InvalidArgument, "new relay resource ID must be empty")
		}
		return xid.New().String(), nil
	}
	if pathID == "" || (bodyID != "" && bodyID != pathID) {
		return "", status.Errorf(status.InvalidArgument, "relay resource ID does not match request path")
	}
	return pathID, nil
}

func mutateResource(w http.ResponseWriter, r *http.Request, candidate *rp.Configuration, kind resourceKind, id string, account rp.AccountID) (uint64, uint64, error) {
	switch kind {
	case groupKind:
		var request api.RelayGroupMutation
		if err := decode(w, r, &request); err != nil {
			return 0, 0, err
		}
		resourceID, err := mutationID(r, id, request.Resource.Id)
		if err != nil {
			return 0, 0, err
		}
		value := fromGroup(request.Resource, account)
		value.ID = rp.RelayGroupID(resourceID)
		index := slices.IndexFunc(candidate.Groups, func(existing rp.RelayGroup) bool { return existing.ID == value.ID })
		switch {
		case r.Method == http.MethodPost:
			candidate.Groups = append(candidate.Groups, value)
		case index < 0:
			return 0, 0, status.Errorf(status.NotFound, "relay group not found")
		default:
			candidate.Groups[index] = value
		}
		return request.ExpectedRevision, request.CatalogGeneration, nil
	case profileKind:
		var request api.RelayHomeProfileMutation
		if err := decode(w, r, &request); err != nil {
			return 0, 0, err
		}
		resourceID, err := mutationID(r, id, request.Resource.Id)
		if err != nil {
			return 0, 0, err
		}
		value := fromProfile(request.Resource, account)
		value.ID = rp.RelayProfileID(resourceID)
		index := slices.IndexFunc(candidate.Profiles, func(existing rp.RelayHomeProfile) bool { return existing.ID == value.ID })
		switch {
		case r.Method == http.MethodPost:
			candidate.Profiles = append(candidate.Profiles, value)
		case index < 0:
			return 0, 0, status.Errorf(status.NotFound, "home profile not found")
		default:
			candidate.Profiles[index] = value
		}
		return request.ExpectedRevision, request.CatalogGeneration, nil
	case pairKind:
		var request api.RelayPairPolicyMutation
		if err := decode(w, r, &request); err != nil {
			return 0, 0, err
		}
		resourceID, err := mutationID(r, id, request.Resource.Id)
		if err != nil {
			return 0, 0, err
		}
		value := fromPairPolicy(request.Resource, account)
		value.ID = rp.PolicyID(resourceID)
		index := slices.IndexFunc(candidate.Policies, func(existing rp.RelayPairPolicy) bool { return existing.ID == value.ID })
		switch {
		case r.Method == http.MethodPost:
			candidate.Policies = append(candidate.Policies, value)
		case index < 0:
			return 0, 0, status.Errorf(status.NotFound, "pair policy not found")
		default:
			candidate.Policies[index] = value
		}
		return request.ExpectedRevision, request.CatalogGeneration, nil
	default:
		return 0, 0, status.Errorf(status.InvalidArgument, "unknown relay resource")
	}
}

func deleteResource(candidate *rp.Configuration, kind resourceKind, id string) error {
	switch kind {
	case groupKind:
		index := slices.IndexFunc(candidate.Groups, func(value rp.RelayGroup) bool { return string(value.ID) == id })
		if index < 0 {
			return status.Errorf(status.NotFound, "relay group not found")
		}
		candidate.Groups = slices.Delete(candidate.Groups, index, index+1)
	case profileKind:
		index := slices.IndexFunc(candidate.Profiles, func(value rp.RelayHomeProfile) bool { return string(value.ID) == id })
		if index < 0 {
			return status.Errorf(status.NotFound, "home profile not found")
		}
		candidate.Profiles = slices.Delete(candidate.Profiles, index, index+1)
	case pairKind:
		index := slices.IndexFunc(candidate.Policies, func(value rp.RelayPairPolicy) bool { return string(value.ID) == id })
		if index < 0 {
			return status.Errorf(status.NotFound, "pair policy not found")
		}
		candidate.Policies = slices.Delete(candidate.Policies, index, index+1)
	default:
		return status.Errorf(status.InvalidArgument, "unknown relay resource")
	}
	return nil
}

type page struct {
	limit, offset int
	name          string
}

func pagination(r *http.Request) (page, error) {
	result := page{limit: 100, name: strings.ToLower(r.URL.Query().Get("name"))}
	for _, field := range []struct {
		name     string
		target   *int
		min, max int
	}{{"limit", &result.limit, 1, 200}, {"offset", &result.offset, 0, 1000000}} {
		if raw := r.URL.Query().Get(field.name); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < field.min || value > field.max {
				return result, status.Errorf(status.InvalidArgument, "invalid %s", field.name)
			}
			*field.target = value
		}
	}
	if len(result.name) > 255 {
		return result, status.Errorf(status.InvalidArgument, "name filter is too long")
	}
	return result, nil
}

func window[T any](values []T, p page) []T {
	start := min(p.offset, len(values))
	return values[start:min(start+p.limit, len(values))]
}

func (h *handler) peerSelectorFilter(r *http.Request, accountID string) (func(rp.RelaySelector) bool, error) {
	peerID := rp.PeerID(r.URL.Query().Get("peer_id"))
	if peerID == "" {
		return func(rp.RelaySelector) bool { return true }, nil
	}
	account, err := h.store.GetAccount(r.Context(), accountID)
	if err != nil {
		return nil, err
	}
	if _, exists := account.Peers[string(peerID)]; !exists {
		return nil, status.Errorf(status.NotFound, "account peer not found")
	}
	groups := make(map[rp.PeerGroupID]struct{})
	for _, group := range account.Groups {
		if slices.Contains(group.Peers, string(peerID)) {
			groups[rp.PeerGroupID(group.ID)] = struct{}{}
		}
	}
	return func(selector rp.RelaySelector) bool {
		if slices.Contains(selector.PeerIDs, peerID) {
			return true
		}
		for _, id := range selector.GroupIDs {
			if _, exists := groups[id]; exists {
				return true
			}
		}
		return false
	}, nil
}

func (h *handler) listResources(w http.ResponseWriter, r *http.Request, kind resourceKind, snapshot rp.Snapshot, accountID string) {
	p, err := pagination(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	matches, err := h.peerSelectorFilter(r, accountID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	switch kind {
	case groupKind:
		if r.URL.Query().Get("peer_id") != "" {
			writeError(w, r, status.Errorf(status.InvalidArgument, "peer filter applies to profiles and pair policies"))
			return
		}
		items := make([]api.RelayGroup, 0)
		for _, value := range snapshot.Groups {
			if strings.Contains(strings.ToLower(value.Name), p.name) {
				items = append(items, group(value))
			}
		}
		util.WriteJSONObject(r.Context(), w, api.RelayGroupList{Revision: snapshot.Revision, CatalogGeneration: snapshot.Catalog.Generation, Total: len(items), Items: window(items, p)})
	case profileKind:
		items := make([]api.RelayHomeProfile, 0)
		for _, value := range snapshot.Profiles {
			if strings.Contains(strings.ToLower(value.Name), p.name) && matches(value.Selector) {
				items = append(items, profile(value))
			}
		}
		util.WriteJSONObject(r.Context(), w, api.RelayHomeProfileList{Revision: snapshot.Revision, CatalogGeneration: snapshot.Catalog.Generation, Total: len(items), Items: window(items, p)})
	case pairKind:
		items := make([]api.RelayPairPolicy, 0)
		for _, value := range snapshot.Policies {
			if strings.Contains(strings.ToLower(value.Name), p.name) && (matches(value.Source) || matches(value.Destination)) {
				items = append(items, pairPolicy(value))
			}
		}
		util.WriteJSONObject(r.Context(), w, api.RelayPairPolicyList{Revision: snapshot.Revision, CatalogGeneration: snapshot.Catalog.Generation, Total: len(items), Items: window(items, p)})
	}
}
