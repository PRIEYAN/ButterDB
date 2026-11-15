package bucketops

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

type policyStatement struct {
	Sid       string `json:"Sid"`
	Effect    string `json:"Effect"`
	Principal string `json:"Principal"`
	Action    string `json:"Action"`
	Resource  string `json:"Resource"`
}

type policyDocument struct {
	Version   string            `json:"Version"`
	Statement []policyStatement `json:"Statement"`
}

func publicReadPolicy(name bucket.BucketName) ([]byte, error) {
	return json.Marshal(policyDocument{
		Version: "2012-10-17",
		Statement: []policyStatement{{
			Sid:       "butterdb-public-read",
			Effect:    "Allow",
			Principal: "*",
			Action:    "s3:GetObject",
			Resource:  "arn:aws:s3:::" + string(name) + "/*",
		}},
	})
}

func (h *Handlers) GetBucketPolicy(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	info, err := h.authorize(r, rt, a)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	if info.Policy != bucket.PolicyPublicRead {
		h.failWith(w, r, rt, s3err.CodeNoSuchBucketPolicy, "")
		return
	}
	document, err := publicReadPolicy(bucket.BucketName(rt.Target.Bucket))
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	header := w.Header()
	header.Set("Content-Type", "application/json")
	header.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(document)
}

func (h *Handlers) GetBucketPolicyStatus(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	info, err := h.authorize(r, rt, a)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	h.respond(w, r, rt, policyStatus{IsPublic: info.Policy == bucket.PolicyPublicRead})
}

func (h *Handlers) GetBucketCors(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	if _, err := h.authorize(r, rt, a); err != nil {
		h.fail(w, r, rt, err)
		return
	}
	cors := h.deps.Config.CORS
	if !cors.Enabled() {
		h.failWith(w, r, rt, s3err.CodeNoSuchCORSConfiguration, "")
		return
	}
	h.respond(w, r, rt, corsConfiguration{Rule: corsRule{
		AllowedOrigins: cors.AllowedOrigins,
		AllowedMethods: cors.AllowedMethods,
		AllowedHeaders: cors.AllowedHeaders,
		ExposeHeaders:  cors.ExposedHeaders(),
		MaxAgeSeconds:  cors.MaxAgeSeconds,
	}})
}

func (h *Handlers) GetBucketVersioning(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	if _, err := h.authorize(r, rt, a); err != nil {
		h.fail(w, r, rt, err)
		return
	}
	h.respond(w, r, rt, versioningConfiguration{})
}

func (h *Handlers) GetBucketEncryption(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	info, err := h.authorize(r, rt, a)
	if err != nil {
		h.fail(w, r, rt, err)
		return
	}
	if info.DefaultEncryption != bucket.EncryptionTDE1 {
		h.failWith(w, r, rt, s3err.CodeServerSideEncryptionConfigurationNotFoundError, "")
		return
	}
	h.respond(w, r, rt, sseConfiguration{Rule: sseRule{Default: sseDefault{Algorithm: "AES256"}}})
}

func (h *Handlers) GetBucketConfig(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	if _, err := h.authorize(r, rt, a); err != nil {
		h.fail(w, r, rt, err)
		return
	}
	if code, absent := absentConfigurations()[rt.Subresource]; absent {
		h.failWith(w, r, rt, code, "")
		return
	}
	if answer, fixed := fixedConfigurations()[rt.Subresource]; fixed {
		h.respond(w, r, rt, answer)
		return
	}
	router.WriteError(w, r, rt.Target, errUnsupportedConfig(rt.Subresource))
}

func absentConfigurations() map[string]s3err.Code {
	return map[string]s3err.Code{
		"lifecycle":         s3err.CodeNoSuchLifecycleConfiguration,
		"tagging":           s3err.CodeNoSuchTagSet,
		"website":           s3err.CodeNoSuchWebsiteConfiguration,
		"object-lock":       s3err.CodeObjectLockConfigurationNotFoundError,
		"publicAccessBlock": s3err.CodeNoSuchPublicAccessBlockConfiguration,
	}
}

func fixedConfigurations() map[string]any {
	return map[string]any{
		"logging":           loggingStatus{},
		"requestPayment":    requestPayment{Payer: "BucketOwner"},
		"ownershipControls": ownershipControls{Rule: ownershipRule{Ownership: "BucketOwnerEnforced"}},
		"notification":      notificationConfiguration{},
		"accelerate":        accelerateConfiguration{},
	}
}

func errUnsupportedConfig(subresource string) error {
	return fmt.Errorf("bucketops: %w: bucket subresource %s", errs.ErrNotImplemented, subresource)
}
