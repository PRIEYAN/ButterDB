package handlers

import (
	"net/http"
	"sort"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
)

func (a *api) registerBucketRoutes() {
	a.authed("GET "+adminv1.PathBuckets, a.listBuckets)
	a.authed("POST "+adminv1.PathBuckets, a.createBucket)
	a.authed("GET "+adminv1.PathBucket, a.getBucket)
	a.authed("PATCH "+adminv1.PathBucket, a.updateBucket)
	a.authed("DELETE "+adminv1.PathBucket, a.deleteBucket)
}

func bucketParam(r *http.Request) (bucket.BucketName, error) {
	name := bucket.BucketName(r.PathValue(adminv1.ParamBucket))
	if err := name.Validate(); err != nil {
		return "", err
	}
	return name, nil
}

func (a *api) listBuckets(w http.ResponseWriter, r *http.Request) error {
	infos, err := a.deps.Buckets.List(r.Context())
	if err != nil {
		return err
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	list := adminv1.BucketList{Buckets: make([]adminv1.Bucket, 0, len(infos))}
	for _, info := range infos {
		list.Buckets = append(list.Buckets, bucketDTO(info))
	}
	a.writeJSON(w, r, http.StatusOK, list)
	return nil
}

func (a *api) createBucket(w http.ResponseWriter, r *http.Request) error {
	var req adminv1.CreateBucketRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	spec, err := specFromRequest(req)
	if err != nil {
		return err
	}
	info, err := a.deps.Buckets.Create(r.Context(), spec)
	a.record(r, "bucket.create", string(spec.Name), err)
	if err != nil {
		return err
	}
	a.writeJSON(w, r, http.StatusCreated, bucketDTO(info))
	return nil
}

func specFromRequest(req adminv1.CreateBucketRequest) (bucket.Spec, error) {
	policy := req.Policy
	if policy == "" {
		policy = bucket.PolicyPrivate
	}
	encryption := req.EncryptionDefault
	if encryption == "" {
		encryption = bucket.EncryptionNone
	}
	cacheMaxAge := int64(bucket.DefaultCacheMaxAgeSeconds)
	if req.CacheMaxAgeSeconds != nil {
		cacheMaxAge = int64(*req.CacheMaxAgeSeconds)
	}
	return bucket.NewSpec(req.Name, policy, encryption, cacheMaxAge)
}

func (a *api) getBucket(w http.ResponseWriter, r *http.Request) error {
	name, err := bucketParam(r)
	if err != nil {
		return err
	}
	info, err := a.deps.Buckets.Get(r.Context(), name)
	if err != nil {
		return err
	}
	a.writeJSON(w, r, http.StatusOK, bucketDTO(info))
	return nil
}

func (a *api) updateBucket(w http.ResponseWriter, r *http.Request) error {
	name, err := bucketParam(r)
	if err != nil {
		return err
	}
	var req adminv1.UpdateBucketRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	patch := bucket.Patch{Policy: req.Policy, DefaultEncryption: req.EncryptionDefault}
	if req.CacheMaxAgeSeconds != nil {
		seconds := int64(*req.CacheMaxAgeSeconds)
		patch.CacheMaxAge = &seconds
	}
	if patch.IsEmpty() {
		return &ValidationError{Field: "body", Reason: "at least one field is required"}
	}
	if err := patch.Validate(); err != nil {
		return err
	}
	info, err := a.deps.Buckets.Update(r.Context(), name, patch)
	a.record(r, "bucket.update", string(name), err)
	if err != nil {
		return err
	}
	a.writeJSON(w, r, http.StatusOK, bucketDTO(info))
	return nil
}

func (a *api) deleteBucket(w http.ResponseWriter, r *http.Request) error {
	name, err := bucketParam(r)
	if err != nil {
		return err
	}
	err = a.deps.Buckets.Delete(r.Context(), name)
	a.record(r, "bucket.delete", string(name), err)
	if err != nil {
		return err
	}
	a.writeNoContent(w)
	return nil
}
