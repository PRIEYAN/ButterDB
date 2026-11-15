package router

import (
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
)

type dispatchCase struct {
	name        string
	method      string
	target      string
	header      map[string]string
	op          Operation
	subresource string
	status      int
	code        s3err.Code
}

func dispatchCases() []dispatchCase {
	copyHeader := map[string]string{"X-Amz-Copy-Source": "/photos/a"}
	cases := []dispatchCase{
		{name: "GET service", method: "GET", target: "/", op: OpListBuckets},
		{name: "POST service", method: "POST", target: "/", status: 405, code: s3err.CodeMethodNotAllowed},
		{name: "PUT service", method: "PUT", target: "/", status: 405, code: s3err.CodeMethodNotAllowed},
		{name: "DELETE service", method: "DELETE", target: "/", status: 405, code: s3err.CodeMethodNotAllowed},
		{name: "HEAD service", method: "HEAD", target: "/", status: 405, code: s3err.CodeMethodNotAllowed},

		{name: "GET bucket location", method: "GET", target: "/photos?location", op: OpGetBucketLocation, subresource: "location"},
		{name: "GET bucket uploads", method: "GET", target: "/photos?uploads", op: OpListMultipartUploads, subresource: "uploads"},
		{name: "GET bucket versions", method: "GET", target: "/photos?versions", status: 501, code: s3err.CodeNotImplemented},
		{name: "GET bucket list type 2", method: "GET", target: "/photos?list-type=2", op: OpListObjectsV2},
		{name: "GET bucket list type 1", method: "GET", target: "/photos?list-type=1", status: 400, code: s3err.CodeInvalidArgument},
		{name: "GET bucket list type empty", method: "GET", target: "/photos?list-type=", status: 400, code: s3err.CodeInvalidArgument},
		{name: "GET bucket none", method: "GET", target: "/photos", op: OpListObjects},
		{name: "GET bucket listing parameters", method: "GET", target: "/photos?prefix=a&delimiter=%2F&max-keys=5&marker=b", op: OpListObjects},
		{name: "GET bucket subresource with empty value", method: "GET", target: "/photos?acl=", op: OpGetBucketAcl, subresource: "acl"},

		{name: "HEAD bucket", method: "HEAD", target: "/photos", op: OpHeadBucket},
		{name: "PUT bucket acl", method: "PUT", target: "/photos?acl", op: OpPutBucketAcl, subresource: "acl"},
		{name: "PUT bucket policy", method: "PUT", target: "/photos?policy", status: 501, code: s3err.CodeNotImplemented},
		{name: "PUT bucket versioning", method: "PUT", target: "/photos?versioning", status: 501, code: s3err.CodeNotImplemented},
		{name: "PUT bucket cors", method: "PUT", target: "/photos?cors", status: 501, code: s3err.CodeNotImplemented},
		{name: "PUT bucket none", method: "PUT", target: "/photos", op: OpCreateBucket},
		{name: "DELETE bucket policy", method: "DELETE", target: "/photos?policy", status: 501, code: s3err.CodeNotImplemented},
		{name: "DELETE bucket cors", method: "DELETE", target: "/photos?cors", status: 501, code: s3err.CodeNotImplemented},
		{name: "DELETE bucket none", method: "DELETE", target: "/photos", op: OpDeleteBucket},
		{name: "POST bucket delete", method: "POST", target: "/photos?delete", op: OpDeleteObjects, subresource: "delete"},
		{name: "POST bucket none", method: "POST", target: "/photos", status: 501, code: s3err.CodeNotImplemented},
		{name: "POST bucket other", method: "POST", target: "/photos?uploads", status: 501, code: s3err.CodeNotImplemented},
		{name: "PATCH bucket", method: "PATCH", target: "/photos", status: 405, code: s3err.CodeMethodNotAllowed},

		{name: "GET object uploadId", method: "GET", target: "/photos/a?uploadId=U", op: OpListParts, subresource: "uploadId"},
		{name: "GET object acl", method: "GET", target: "/photos/a?acl", op: OpGetObjectAcl, subresource: "acl"},
		{name: "GET object tagging", method: "GET", target: "/photos/a?tagging", op: OpGetObjectTagging, subresource: "tagging"},
		{name: "GET object attributes", method: "GET", target: "/photos/a?attributes", status: 501, code: s3err.CodeNotImplemented},
		{name: "GET object torrent", method: "GET", target: "/photos/a?torrent", status: 501, code: s3err.CodeNotImplemented},
		{name: "GET object retention", method: "GET", target: "/photos/a?retention", status: 501, code: s3err.CodeNotImplemented},
		{name: "GET object legal-hold", method: "GET", target: "/photos/a?legal-hold", status: 501, code: s3err.CodeNotImplemented},
		{name: "GET object none", method: "GET", target: "/photos/a", op: OpGetObject},
		{name: "HEAD object", method: "HEAD", target: "/photos/a", op: OpHeadObject},
		{name: "PUT object part and upload copy", method: "PUT", target: "/photos/a?partNumber=1&uploadId=U", header: copyHeader, op: OpUploadPartCopy, subresource: "uploadId"},
		{name: "PUT object part and upload", method: "PUT", target: "/photos/a?partNumber=1&uploadId=U", op: OpUploadPart, subresource: "uploadId"},
		{name: "PUT object part only", method: "PUT", target: "/photos/a?partNumber=1", status: 400, code: s3err.CodeInvalidArgument},
		{name: "PUT object upload only", method: "PUT", target: "/photos/a?uploadId=U", status: 400, code: s3err.CodeInvalidArgument},
		{name: "PUT object acl", method: "PUT", target: "/photos/a?acl", op: OpPutObjectAcl, subresource: "acl"},
		{name: "PUT object tagging", method: "PUT", target: "/photos/a?tagging", op: OpPutObjectTagging, subresource: "tagging"},
		{name: "PUT object retention", method: "PUT", target: "/photos/a?retention", status: 501, code: s3err.CodeNotImplemented},
		{name: "PUT object legal-hold", method: "PUT", target: "/photos/a?legal-hold", status: 501, code: s3err.CodeNotImplemented},
		{name: "PUT object copy source", method: "PUT", target: "/photos/a", header: copyHeader, op: OpCopyObject},
		{name: "PUT object none", method: "PUT", target: "/photos/a", op: OpPutObject},
		{name: "DELETE object uploadId", method: "DELETE", target: "/photos/a?uploadId=U", op: OpAbortMultipartUpload, subresource: "uploadId"},
		{name: "DELETE object tagging", method: "DELETE", target: "/photos/a?tagging", op: OpDeleteObjectTagging, subresource: "tagging"},
		{name: "DELETE object none", method: "DELETE", target: "/photos/a", op: OpDeleteObject},
		{name: "POST object uploads", method: "POST", target: "/photos/a?uploads", op: OpCreateMultipartUpload, subresource: "uploads"},
		{name: "POST object uploadId", method: "POST", target: "/photos/a?uploadId=U", op: OpCompleteMultipartUpload, subresource: "uploadId"},
		{name: "POST object restore", method: "POST", target: "/photos/a?restore", status: 501, code: s3err.CodeNotImplemented},
		{name: "POST object select", method: "POST", target: "/photos/a?select", status: 501, code: s3err.CodeNotImplemented},
		{name: "POST object none", method: "POST", target: "/photos/a", status: 405, code: s3err.CodeMethodNotAllowed},
		{name: "PATCH object", method: "PATCH", target: "/photos/a", status: 405, code: s3err.CodeMethodNotAllowed},

		{name: "OPTIONS service", method: "OPTIONS", target: "/", op: OpPreflight},
		{name: "OPTIONS bucket", method: "OPTIONS", target: "/photos", op: OpPreflight},
		{name: "OPTIONS object", method: "OPTIONS", target: "/photos/a", op: OpPreflight},
	}
	for _, name := range []string{"versioning", "acl", "policy", "policyStatus", "cors", "encryption", "tagging", "lifecycle", "website", "logging", "requestPayment", "object-lock", "publicAccessBlock", "ownershipControls", "notification", "accelerate"} {
		cases = append(cases, dispatchCase{name: "GET bucket getter " + name, method: "GET", target: "/photos?" + name, op: getterOperation(name), subresource: name})
	}
	for _, name := range []string{"replication", "analytics", "inventory", "metrics", "intelligent-tiering", "metadataTable", "session"} {
		cases = append(cases, dispatchCase{name: "GET bucket unsupported " + name, method: "GET", target: "/photos?" + name, status: 501, code: s3err.CodeNotImplemented})
	}
	return cases
}

func getterOperation(name string) Operation {
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

func TestDispatchTable(t *testing.T) {
	for _, tc := range dispatchCases() {
		t.Run(tc.name, func(t *testing.T) {
			r := newRequest(tc.method, "localhost", tc.target)
			for name, value := range tc.header {
				r.Header.Set(name, value)
			}
			target, err := ParseTarget(r, Addressing{})
			if err != nil {
				t.Fatalf("ParseTarget: %v", err)
			}
			got, err := Resolve(r, target)
			if tc.code != "" {
				mapped := s3err.Map(err)
				if err == nil || mapped.Code != tc.code || mapped.Status != tc.status {
					t.Fatalf("Resolve = %+v, %v; want %s/%d", got, err, tc.code, tc.status)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Operation != tc.op || got.Subresource != tc.subresource || got.Target != target {
				t.Fatalf("Resolve = %+v, want %s with subresource %q", got, tc.op, tc.subresource)
			}
		})
	}
}

func TestXIDIgnored(t *testing.T) {
	cases := []struct {
		name   string
		method string
		target string
		op     Operation
	}{
		{"get object x-id", "GET", "/photos/a?x-id=GetObject", OpGetObject},
		{"put object x-id", "PUT", "/photos/a?x-id=PutObject", OpPutObject},
		{"delete object x-id", "DELETE", "/photos/a?x-id=DeleteObject", OpDeleteObject},
		{"list objects x-id", "GET", "/photos?x-id=ListObjects", OpListObjects},
		{"list v2 x-id", "GET", "/photos?list-type=2&x-id=ListObjectsV2", OpListObjectsV2},
		{"response overrides", "GET", "/photos/a?response-content-type=text%2Fplain&response-content-disposition=attachment", OpGetObject},
		{"presign parameters", "GET", "/photos/a?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Signature=00", OpGetObject},
		{"version id null", "GET", "/photos/a?versionId=null", OpGetObject},
		{"upload part with x-id", "PUT", "/photos/a?partNumber=2&uploadId=U&x-id=UploadPart", OpUploadPart},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRequest(tc.method, "localhost", tc.target)
			target, err := ParseTarget(r, Addressing{})
			if err != nil {
				t.Fatalf("ParseTarget: %v", err)
			}
			got, err := Resolve(r, target)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Operation != tc.op || got.Subresource != "" && tc.op != OpUploadPart {
				t.Fatalf("Resolve = %+v, want %s", got, tc.op)
			}
		})
	}
}

func TestActionTable(t *testing.T) {
	want := map[Operation]access.Action{
		OpHeadBucket: access.ActionList, OpGetBucketLocation: access.ActionList, OpListObjectsV2: access.ActionList,
		OpListObjects: access.ActionList, OpListMultipartUploads: access.ActionList, OpListParts: access.ActionList,
		OpGetBucketVersioning: access.ActionList, OpGetBucketPolicy: access.ActionList, OpGetBucketPolicyStatus: access.ActionList,
		OpGetBucketAcl: access.ActionList, OpGetBucketCors: access.ActionList, OpGetBucketEncryption: access.ActionList,
		OpGetBucketConfig: access.ActionList,
		OpGetObject:       access.ActionRead, OpHeadObject: access.ActionRead, OpGetObjectAcl: access.ActionRead, OpGetObjectTagging: access.ActionRead,
		OpPutObject: access.ActionWrite, OpCopyObject: access.ActionWrite, OpCreateMultipartUpload: access.ActionWrite,
		OpUploadPart: access.ActionWrite, OpUploadPartCopy: access.ActionWrite, OpCompleteMultipartUpload: access.ActionWrite,
		OpAbortMultipartUpload: access.ActionWrite, OpPutObjectAcl: access.ActionWrite, OpPutBucketAcl: access.ActionWrite,
		OpPutObjectTagging: access.ActionWrite, OpDeleteObjectTagging: access.ActionWrite,
		OpDeleteObject: access.ActionDelete, OpDeleteObjects: access.ActionDelete,
	}
	got := Actions()
	if len(got) != len(want) {
		t.Fatalf("Actions has %d entries, want %d", len(got), len(want))
	}
	for op, action := range want {
		if got[op] != action {
			t.Fatalf("Actions[%s] = %q, want %q", op, got[op], action)
		}
	}
	for _, op := range []Operation{OpListBuckets, OpCreateBucket, OpDeleteBucket, OpPreflight} {
		if _, ok := ActionFor(op); ok {
			t.Fatalf("%s must have no bucket action", op)
		}
	}
	got[OpPutObject] = access.ActionRead
	if again, _ := ActionFor(OpPutObject); again != access.ActionWrite {
		t.Fatal("Actions returned a shared map")
	}
}
