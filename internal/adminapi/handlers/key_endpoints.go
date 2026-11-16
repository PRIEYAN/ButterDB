package handlers

import (
	"net/http"
	"sort"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
)

func (a *api) registerKeyRoutes() {
	a.authed("GET "+adminv1.PathAccessKeys, a.listAccessKeys)
	a.authed("POST "+adminv1.PathAccessKeys, a.createAccessKey)
	a.authed("GET "+adminv1.PathAccessKey, a.getAccessKey)
	a.authed("PATCH "+adminv1.PathAccessKey, a.updateAccessKey)
	a.authed("DELETE "+adminv1.PathAccessKey, a.revokeAccessKey)
}

func accessKeyParam(r *http.Request) (access.AccessKeyID, error) {
	return access.ParseAccessKeyID(r.PathValue(adminv1.ParamAccessKeyID))
}

func scopeFromDTO(dto adminv1.Scope) (access.Scope, error) {
	return access.NewScope(dto.Buckets, dto.Actions)
}

func (a *api) listAccessKeys(w http.ResponseWriter, r *http.Request) error {
	includeRevoked, err := parseBoolQuery(r.URL.Query().Get(adminv1.QueryIncludeRevoked))
	if err != nil {
		return &BadRequestError{Field: adminv1.QueryIncludeRevoked}
	}
	infos, err := a.deps.Keys.List(r.Context(), includeRevoked)
	if err != nil {
		return err
	}
	sort.SliceStable(infos, func(i, j int) bool { return infos[i].CreatedAt > infos[j].CreatedAt })
	list := adminv1.AccessKeyList{AccessKeys: make([]adminv1.AccessKey, 0, len(infos))}
	for _, info := range infos {
		list.AccessKeys = append(list.AccessKeys, accessKeyDTO(info))
	}
	a.writeJSON(w, r, http.StatusOK, list)
	return nil
}

func (a *api) createAccessKey(w http.ResponseWriter, r *http.Request) error {
	var req adminv1.CreateAccessKeyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	if err := access.ValidateKeyName(req.Name); err != nil {
		return err
	}
	scope, err := scopeFromDTO(req.Scope)
	if err != nil {
		return err
	}
	info, secret, err := a.deps.Keys.Create(r.Context(), req.Name, scope)
	a.record(r, "access_key.create", info.ID.String(), err)
	if err != nil {
		return err
	}
	a.writeJSON(w, r, http.StatusCreated, adminv1.AccessKeyCreated{
		AccessKey:       accessKeyDTO(info),
		SecretAccessKey: secret,
		S3Endpoint:      a.deps.Config.S3Endpoint,
		S3Region:        a.deps.Config.S3Region,
	})
	return nil
}

func (a *api) getAccessKey(w http.ResponseWriter, r *http.Request) error {
	id, err := accessKeyParam(r)
	if err != nil {
		return err
	}
	info, err := a.deps.Keys.Get(r.Context(), id)
	if err != nil {
		return err
	}
	a.writeJSON(w, r, http.StatusOK, accessKeyDTO(info))
	return nil
}

func (a *api) updateAccessKey(w http.ResponseWriter, r *http.Request) error {
	id, err := accessKeyParam(r)
	if err != nil {
		return err
	}
	var req adminv1.UpdateAccessKeyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	if req.Name == nil && req.Scope == nil {
		return &ValidationError{Field: "body", Reason: "at least one field is required"}
	}
	if req.Name != nil {
		if err := access.ValidateKeyName(*req.Name); err != nil {
			return err
		}
	}
	var scope *access.Scope
	if req.Scope != nil {
		parsed, err := scopeFromDTO(*req.Scope)
		if err != nil {
			return err
		}
		scope = &parsed
	}
	info, err := a.deps.Keys.Update(r.Context(), id, req.Name, scope)
	a.record(r, "access_key.update", id.String(), err)
	if err != nil {
		return err
	}
	a.writeJSON(w, r, http.StatusOK, accessKeyDTO(info))
	return nil
}

func (a *api) revokeAccessKey(w http.ResponseWriter, r *http.Request) error {
	id, err := accessKeyParam(r)
	if err != nil {
		return err
	}
	info, err := a.deps.Keys.Revoke(r.Context(), id)
	a.record(r, "access_key.revoke", id.String(), err)
	if err != nil {
		return err
	}
	a.writeJSON(w, r, http.StatusOK, accessKeyDTO(info))
	return nil
}
