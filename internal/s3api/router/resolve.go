package router

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

const copySourceHeader = "X-Amz-Copy-Source"

func Resolve(r *http.Request, t Target) (Route, error) {
	if r.Method == http.MethodOptions {
		return Route{Target: t, Operation: OpPreflight}, nil
	}
	query := r.URL.Query()
	switch {
	case t.Bucket == "":
		return resolveService(r, t)
	case !t.HasKey:
		return resolveBucket(r, t, query)
	}
	return resolveObject(r, t, query)
}

func route(t Target, op Operation, subresource string) (Route, error) {
	return Route{Target: t, Operation: op, Subresource: subresource}, nil
}

func notImplemented(what string) error {
	return fmt.Errorf("router: %w: %s", errs.ErrNotImplemented, what)
}

func methodNotAllowed() error {
	return s3err.New(s3err.CodeMethodNotAllowed, "")
}

func invalidArgument(message string) error {
	return s3err.New(s3err.CodeInvalidArgument, message)
}

func resolveService(r *http.Request, t Target) (Route, error) {
	if r.Method != http.MethodGet {
		return Route{}, methodNotAllowed()
	}
	return route(t, OpListBuckets, "")
}

func firstPresent(query url.Values, names ...string) (string, bool) {
	for _, name := range names {
		if _, ok := query[name]; ok {
			return name, true
		}
	}
	return "", false
}

func resolveBucket(r *http.Request, t Target, query url.Values) (Route, error) {
	switch r.Method {
	case http.MethodGet:
		return resolveBucketGet(t, query)
	case http.MethodHead:
		return route(t, OpHeadBucket, "")
	case http.MethodPut:
		return resolveBucketPut(t, query)
	case http.MethodDelete:
		return resolveBucketDelete(t, query)
	case http.MethodPost:
		return resolveBucketPost(t, query)
	}
	return Route{}, methodNotAllowed()
}

func resolveBucketGet(t Target, query url.Values) (Route, error) {
	if name, ok := firstPresent(query, "location"); ok {
		return route(t, OpGetBucketLocation, name)
	}
	if name, ok := firstPresent(query, "uploads"); ok {
		return route(t, OpListMultipartUploads, name)
	}
	if _, ok := firstPresent(query, "versions"); ok {
		return Route{}, notImplemented("ListObjectVersions")
	}
	if name, ok := firstPresent(query, bucketGetterNames()...); ok {
		return route(t, bucketGetterOperation(name), name)
	}
	if name, ok := firstPresent(query, unsupportedBucketGetterNames()...); ok {
		return Route{}, notImplemented("bucket subresource " + name)
	}
	return resolveBucketListing(t, query)
}

func resolveBucketListing(t Target, query url.Values) (Route, error) {
	listType, present := query["list-type"]
	if !present {
		return route(t, OpListObjects, "")
	}
	if len(listType) == 1 && listType[0] == "2" {
		return route(t, OpListObjectsV2, "")
	}
	return Route{}, invalidArgument("Invalid list-type: only 2 is supported")
}

func bucketGetterNames() []string {
	return []string{
		"versioning", "acl", "policy", "policyStatus", "cors", "encryption", "tagging",
		"lifecycle", "website", "logging", "requestPayment", "object-lock",
		"publicAccessBlock", "ownershipControls", "notification", "accelerate",
	}
}

func unsupportedBucketGetterNames() []string {
	return []string{"replication", "analytics", "inventory", "metrics", "intelligent-tiering", "metadataTable", "session"}
}

func bucketGetterOperation(name string) Operation {
	switch name {
	case "versioning":
		return OpGetBucketVersioning
	case "acl":
		return OpGetBucketAcl
	case "policy":
		return OpGetBucketPolicy
	case "policyStatus":
		return OpGetBucketPolicyStatus
	case "cors":
		return OpGetBucketCors
	case "encryption":
		return OpGetBucketEncryption
	}
	return OpGetBucketConfig
}

func allBucketSubresourceNames() []string {
	names := append([]string{"location", "uploads", "versions", "delete"}, bucketGetterNames()...)
	return append(names, unsupportedBucketGetterNames()...)
}

func resolveBucketPut(t Target, query url.Values) (Route, error) {
	if name, ok := firstPresent(query, "acl"); ok {
		return route(t, OpPutBucketAcl, name)
	}
	if name, ok := firstPresent(query, allBucketSubresourceNames()...); ok {
		return Route{}, notImplemented("bucket subresource " + name)
	}
	return route(t, OpCreateBucket, "")
}

func resolveBucketDelete(t Target, query url.Values) (Route, error) {
	if name, ok := firstPresent(query, allBucketSubresourceNames()...); ok {
		return Route{}, notImplemented("bucket subresource " + name)
	}
	return route(t, OpDeleteBucket, "")
}

func resolveBucketPost(t Target, query url.Values) (Route, error) {
	if name, ok := firstPresent(query, "delete"); ok {
		return route(t, OpDeleteObjects, name)
	}
	return Route{}, notImplemented("PostObject")
}

func resolveObject(r *http.Request, t Target, query url.Values) (Route, error) {
	switch r.Method {
	case http.MethodGet:
		return resolveObjectGet(t, query)
	case http.MethodHead:
		return route(t, OpHeadObject, "")
	case http.MethodPut:
		return resolveObjectPut(r, t, query)
	case http.MethodDelete:
		return resolveObjectDelete(t, query)
	case http.MethodPost:
		return resolveObjectPost(t, query)
	}
	return Route{}, methodNotAllowed()
}

func resolveObjectGet(t Target, query url.Values) (Route, error) {
	if name, ok := firstPresent(query, "uploadId"); ok {
		return route(t, OpListParts, name)
	}
	if name, ok := firstPresent(query, "acl"); ok {
		return route(t, OpGetObjectAcl, name)
	}
	if name, ok := firstPresent(query, "tagging"); ok {
		return route(t, OpGetObjectTagging, name)
	}
	if name, ok := firstPresent(query, "attributes", "torrent", "retention", "legal-hold"); ok {
		return Route{}, notImplemented("object subresource " + name)
	}
	return route(t, OpGetObject, "")
}

func resolveObjectPut(r *http.Request, t Target, query url.Values) (Route, error) {
	_, hasPart := query["partNumber"]
	_, hasUpload := query["uploadId"]
	if hasPart != hasUpload {
		return Route{}, invalidArgument("Both partNumber and uploadId are required for UploadPart")
	}
	_, hasCopySource := r.Header[copySourceHeader]
	if hasPart {
		if hasCopySource {
			return route(t, OpUploadPartCopy, "uploadId")
		}
		return route(t, OpUploadPart, "uploadId")
	}
	if name, ok := firstPresent(query, "acl"); ok {
		return route(t, OpPutObjectAcl, name)
	}
	if name, ok := firstPresent(query, "tagging"); ok {
		return route(t, OpPutObjectTagging, name)
	}
	if name, ok := firstPresent(query, "retention", "legal-hold"); ok {
		return Route{}, notImplemented("object subresource " + name)
	}
	if hasCopySource {
		return route(t, OpCopyObject, "")
	}
	return route(t, OpPutObject, "")
}

func resolveObjectDelete(t Target, query url.Values) (Route, error) {
	if name, ok := firstPresent(query, "uploadId"); ok {
		return route(t, OpAbortMultipartUpload, name)
	}
	if name, ok := firstPresent(query, "tagging"); ok {
		return route(t, OpDeleteObjectTagging, name)
	}
	return route(t, OpDeleteObject, "")
}

func resolveObjectPost(t Target, query url.Values) (Route, error) {
	if name, ok := firstPresent(query, "uploads"); ok {
		return route(t, OpCreateMultipartUpload, name)
	}
	if name, ok := firstPresent(query, "uploadId"); ok {
		return route(t, OpCompleteMultipartUpload, name)
	}
	if name, ok := firstPresent(query, "restore", "select"); ok {
		return Route{}, notImplemented("object subresource " + name)
	}
	return Route{}, methodNotAllowed()
}
