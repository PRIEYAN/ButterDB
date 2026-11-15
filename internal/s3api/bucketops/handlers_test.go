package bucketops

import (
	"encoding/xml"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/xmlcodec"
)

func errorCode(t *testing.T, body string) string {
	t.Helper()
	var doc struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("body %q is not an error document: %v", body, err)
	}
	return doc.Code
}

func rootName(t *testing.T, body string) xml.Name {
	t.Helper()
	var root struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal([]byte(body), &root); err != nil {
		t.Fatalf("body %q is not xml: %v", body, err)
	}
	return root.XMLName
}

func TestBucketGettersStatic(t *testing.T) {
	publicEncrypted := map[bucket.BucketName]bucket.Info{"photos": {Name: "photos", Policy: bucket.PolicyPublicRead, DefaultEncryption: bucket.EncryptionTDE1}}
	privatePlain := map[bucket.BucketName]bucket.Info{"photos": {Name: "photos", Policy: bucket.PolicyPrivate, DefaultEncryption: bucket.EncryptionNone}}
	corsOn := router.CORSConfig{AllowedOrigins: []string{"https://app.example.com"}, AllowedMethods: []string{"GET", "PUT"}, AllowedHeaders: []string{"*"}, ExposeHeaders: []string{"ETag"}, MaxAgeSeconds: 3600}
	cases := []struct {
		name       string
		op         router.Operation
		sub        string
		infos      map[bucket.BucketName]bucket.Info
		cors       router.CORSConfig
		wantStatus int
		wantCode   string
		wantRoot   string
		wantInBody []string
	}{
		{"versioning", router.OpGetBucketVersioning, "versioning", nil, router.CORSConfig{}, 200, "", "VersioningConfiguration", nil},
		{"policy public", router.OpGetBucketPolicy, "policy", publicEncrypted, router.CORSConfig{}, 200, "", "", []string{`"Sid":"butterdb-public-read"`, `"Resource":"arn:aws:s3:::photos/*"`}},
		{"policy private", router.OpGetBucketPolicy, "policy", privatePlain, router.CORSConfig{}, 404, "NoSuchBucketPolicy", "", nil},
		{"policy status public", router.OpGetBucketPolicyStatus, "policyStatus", publicEncrypted, router.CORSConfig{}, 200, "", "PolicyStatus", []string{"<IsPublic>true</IsPublic>"}},
		{"policy status private", router.OpGetBucketPolicyStatus, "policyStatus", privatePlain, router.CORSConfig{}, 200, "", "PolicyStatus", []string{"<IsPublic>false</IsPublic>"}},
		{"cors enabled", router.OpGetBucketCors, "cors", nil, corsOn, 200, "", "CORSConfiguration", []string{"<AllowedOrigin>https://app.example.com</AllowedOrigin>", "<AllowedMethod>GET</AllowedMethod><AllowedMethod>PUT</AllowedMethod>", "<AllowedHeader>*</AllowedHeader>", "<ExposeHeader>ETag</ExposeHeader>", "<MaxAgeSeconds>3600</MaxAgeSeconds>"}},
		{"cors disabled", router.OpGetBucketCors, "cors", nil, router.CORSConfig{}, 404, "NoSuchCORSConfiguration", "", nil},
		{"encryption tde1", router.OpGetBucketEncryption, "encryption", publicEncrypted, router.CORSConfig{}, 200, "", "ServerSideEncryptionConfiguration", []string{"<SSEAlgorithm>AES256</SSEAlgorithm>", "<BucketKeyEnabled>false</BucketKeyEnabled>"}},
		{"encryption none", router.OpGetBucketEncryption, "encryption", privatePlain, router.CORSConfig{}, 404, "ServerSideEncryptionConfigurationNotFoundError", "", nil},
		{"lifecycle", router.OpGetBucketConfig, "lifecycle", nil, router.CORSConfig{}, 404, "NoSuchLifecycleConfiguration", "", nil},
		{"bucket tagging", router.OpGetBucketConfig, "tagging", nil, router.CORSConfig{}, 404, "NoSuchTagSet", "", nil},
		{"website", router.OpGetBucketConfig, "website", nil, router.CORSConfig{}, 404, "NoSuchWebsiteConfiguration", "", nil},
		{"object lock", router.OpGetBucketConfig, "object-lock", nil, router.CORSConfig{}, 404, "ObjectLockConfigurationNotFoundError", "", nil},
		{"public access block", router.OpGetBucketConfig, "publicAccessBlock", nil, router.CORSConfig{}, 404, "NoSuchPublicAccessBlockConfiguration", "", nil},
		{"logging", router.OpGetBucketConfig, "logging", nil, router.CORSConfig{}, 200, "", "BucketLoggingStatus", nil},
		{"request payment", router.OpGetBucketConfig, "requestPayment", nil, router.CORSConfig{}, 200, "", "RequestPaymentConfiguration", []string{"<Payer>BucketOwner</Payer>"}},
		{"ownership controls", router.OpGetBucketConfig, "ownershipControls", nil, router.CORSConfig{}, 200, "", "OwnershipControls", []string{"<ObjectOwnership>BucketOwnerEnforced</ObjectOwnership>"}},
		{"notification", router.OpGetBucketConfig, "notification", nil, router.CORSConfig{}, 200, "", "NotificationConfiguration", nil},
		{"accelerate", router.OpGetBucketConfig, "accelerate", nil, router.CORSConfig{}, 200, "", "AccelerateConfiguration", nil},
		{"unknown config subresource", router.OpGetBucketConfig, "bogus", nil, router.CORSConfig{}, 501, "NotImplemented", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{infos: tc.infos, cors: tc.cors})
			handler := f.handlers.Routes()[tc.op]
			rec := call(handler, http.MethodGet, "/photos?"+tc.sub, "", nil, routeFor(tc.op, tc.sub), fullAuth())
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q", got)
			}
			if tc.wantCode != "" {
				if got := errorCode(t, rec.Body.String()); got != tc.wantCode {
					t.Fatalf("code = %s, want %s", got, tc.wantCode)
				}
				return
			}
			if tc.wantRoot != "" {
				name := rootName(t, rec.Body.String())
				if name.Local != tc.wantRoot || name.Space != xmlcodec.Namespace {
					t.Fatalf("root = %+v, want %s in the s3 namespace", name, tc.wantRoot)
				}
			}
			for _, want := range tc.wantInBody {
				if !strings.Contains(rec.Body.String(), want) {
					t.Fatalf("body %q lacks %q", rec.Body.String(), want)
				}
			}
		})
	}
}

func TestGetBucketPolicyDocumentIsExact(t *testing.T) {
	infos := map[bucket.BucketName]bucket.Info{"photos": {Name: "photos", Policy: bucket.PolicyPublicRead}}
	f := newFixture(t, fixtureOptions{infos: infos})
	rec := call(f.handlers.GetBucketPolicy, http.MethodGet, "/photos?policy", "", nil, routeFor(router.OpGetBucketPolicy, "policy"), fullAuth())
	want := `{"Version":"2012-10-17","Statement":[{"Sid":"butterdb-public-read","Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::photos/*"}]}`
	if rec.Body.String() != want {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
}

func TestGetBucketLocation(t *testing.T) {
	cases := []struct {
		name   string
		region string
		want   string
	}{
		{"us-east-1 is empty", "us-east-1", ""},
		{"default region is us-east-1", "", ""},
		{"other region named", "eu-west-1", "eu-west-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{region: tc.region})
			rec := call(f.handlers.GetBucketLocation, http.MethodGet, "/photos?location", "", nil, routeFor(router.OpGetBucketLocation, "location"), fullAuth())
			var doc struct {
				XMLName xml.Name `xml:"LocationConstraint"`
				Value   string   `xml:",chardata"`
			}
			if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
				t.Fatalf("body %q: %v", rec.Body.String(), err)
			}
			if rec.Code != 200 || doc.Value != tc.want || doc.XMLName.Space != xmlcodec.Namespace {
				t.Fatalf("status = %d doc = %+v", rec.Code, doc)
			}
		})
	}
}

func TestHeadBucket(t *testing.T) {
	f := newFixture(t, fixtureOptions{region: "eu-west-1"})
	rec := call(f.handlers.HeadBucket, http.MethodHead, "/photos", "", nil, routeFor(router.OpHeadBucket, ""), fullAuth())
	if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("X-Amz-Bucket-Region") != "eu-west-1" {
		t.Fatalf("status = %d body = %q headers = %v", rec.Code, rec.Body.String(), rec.Header())
	}
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"out of scope", errs.ErrAccessDenied, 403},
		{"missing bucket", errs.ErrNoSuchBucket, 404},
		{"slow down", errs.ErrSlowDown, 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{evalErr: tc.err})
			rec := call(f.handlers.HeadBucket, http.MethodHead, "/photos", "", nil, routeFor(router.OpHeadBucket, ""), fullAuth())
			if rec.Code != tc.status || rec.Body.Len() != 0 {
				t.Fatalf("status = %d body = %q", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("X-Amz-Bucket-Region") != "" {
				t.Fatal("error response carries the bucket region header")
			}
		})
	}
}

func TestGettersAuthorizeWithListAction(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	readOnly := authFor([]string{"photos"}, []string{"read"})
	rec := call(f.handlers.GetBucketVersioning, http.MethodGet, "/photos?versioning", "", nil, routeFor(router.OpGetBucketVersioning, "versioning"), readOnly)
	if rec.Code != 403 {
		t.Fatalf("a key without list got status %d", rec.Code)
	}
	if len(f.evaluator.calls) != 1 || f.evaluator.calls[0] != "list" {
		t.Fatalf("evaluator calls = %v", f.evaluator.calls)
	}
}

func TestCreateBucketDenied(t *testing.T) {
	infos := map[bucket.BucketName]bucket.Info{"photos": {Name: "photos"}, "secret": {Name: "secret"}}
	cases := []struct {
		name       string
		bucket     string
		wantStatus int
		wantCode   string
		wantText   string
	}{
		{"existing in scope", "photos", 409, "BucketAlreadyOwnedByYou", ""},
		{"existing out of scope does not leak", "secret", 403, "AccessDenied", "Bucket creation is available only through the butterDB admin API"},
		{"new bucket", "newbucket", 403, "AccessDenied", "Bucket creation is available only through the butterDB admin API"},
		{"new bucket with wildcard scope", "other", 403, "AccessDenied", "Bucket creation is available only through the butterDB admin API"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{infos: infos})
			auth := authFor([]string{"photos", "other"}, []string{"write"})
			rt := routeFor(router.OpCreateBucket, "")
			rt.Target.Bucket = tc.bucket
			rec := call(f.handlers.CreateBucket, http.MethodPut, "/"+tc.bucket, "", nil, rt, auth)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := errorCode(t, rec.Body.String()); got != tc.wantCode {
				t.Fatalf("code = %s, want %s", got, tc.wantCode)
			}
			if tc.wantText != "" && !strings.Contains(rec.Body.String(), "<Message>"+tc.wantText+"</Message>") {
				t.Fatalf("body = %s", rec.Body.String())
			}
		})
	}
}

func TestCreateBucketAnonymousDenied(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	rec := call(f.handlers.CreateBucket, http.MethodPut, "/photos", "", nil, routeFor(router.OpCreateBucket, ""), anonymousAuth())
	if rec.Code != 403 || errorCode(t, rec.Body.String()) != "AccessDenied" {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteBucketDenied(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	rec := call(f.handlers.DeleteBucket, http.MethodDelete, "/photos", "", nil, routeFor(router.OpDeleteBucket, ""), fullAuth())
	if rec.Code != 403 || errorCode(t, rec.Body.String()) != "AccessDenied" {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Bucket deletion is available only through the butterDB admin API") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestGetBucketAcl(t *testing.T) {
	for _, policyName := range []string{bucket.PolicyPrivate, bucket.PolicyPublicRead} {
		t.Run(policyName, func(t *testing.T) {
			infos := map[bucket.BucketName]bucket.Info{"photos": {Name: "photos", Policy: policyName}}
			f := newFixture(t, fixtureOptions{infos: infos})
			rec := call(f.handlers.GetBucketAcl, http.MethodGet, "/photos?acl", "", nil, routeFor(router.OpGetBucketAcl, "acl"), fullAuth())
			body := rec.Body.String()
			for _, want := range []string{
				"<AccessControlPolicy xmlns=\"" + xmlcodec.Namespace + "\">",
				"<Owner><ID>" + testOwnerID + "</ID><DisplayName>butterdb</DisplayName></Owner>",
				`xsi:type="CanonicalUser"`,
				"<Permission>FULL_CONTROL</Permission>",
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("body %q lacks %q", body, want)
				}
			}
			if strings.Contains(body, "AllUsers") || strings.Count(body, "<Grant>") != 1 {
				t.Fatalf("a bucket ACL has exactly the owner grant: %s", body)
			}
		})
	}
}

func aclBody(grants ...string) string {
	return `<AccessControlPolicy xmlns="` + xmlcodec.Namespace + `"><Owner><ID>` + testOwnerID + `</ID></Owner><AccessControlList>` + strings.Join(grants, "") + `</AccessControlList></AccessControlPolicy>`
}

func ownerGrant(displayName string) string {
	return `<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>` + testOwnerID + `</ID><DisplayName>` + displayName + `</DisplayName></Grantee><Permission>FULL_CONTROL</Permission></Grant>`
}

func allUsersGrant() string {
	return `<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="Group"><URI>http://acs.amazonaws.com/groups/global/AllUsers</URI></Grantee><Permission>READ</Permission></Grant>`
}

func TestPutBucketAcl(t *testing.T) {
	cases := []struct {
		name       string
		headers    map[string]string
		body       string
		bodiesErr  error
		wantStatus int
		wantCode   string
	}{
		{"private canned", map[string]string{"X-Amz-Acl": "private"}, "", nil, 200, ""},
		{"public read refused on buckets", map[string]string{"X-Amz-Acl": "public-read"}, "", nil, 400, "AccessControlListNotSupported"},
		{"bucket owner full control refused on buckets", map[string]string{"X-Amz-Acl": "bucket-owner-full-control"}, "", nil, 400, "AccessControlListNotSupported"},
		{"public read write never", map[string]string{"X-Amz-Acl": "public-read-write"}, "", nil, 400, "AccessControlListNotSupported"},
		{"authenticated read never", map[string]string{"X-Amz-Acl": "authenticated-read"}, "", nil, 400, "AccessControlListNotSupported"},
		{"grant header", map[string]string{"X-Amz-Grant-Read": "uri=\"x\""}, "", nil, 400, "AccessControlListNotSupported"},
		{"grant header with canned acl", map[string]string{"X-Amz-Acl": "private", "X-Amz-Grant-Full-Control": "id=\"x\""}, "", nil, 400, "AccessControlListNotSupported"},
		{"body equals effective acl", nil, aclBody(ownerGrant("butterdb")), nil, 200, ""},
		{"display name ignored", nil, aclBody(ownerGrant("someone else")), nil, 200, ""},
		{"duplicate owner grant", nil, aclBody(ownerGrant("a"), ownerGrant("b")), nil, 200, ""},
		{"extra all users grant", nil, aclBody(ownerGrant("butterdb"), allUsersGrant()), nil, 400, "AccessControlListNotSupported"},
		{"only all users grant", nil, aclBody(allUsersGrant()), nil, 400, "AccessControlListNotSupported"},
		{"no grants", nil, aclBody(), nil, 400, "AccessControlListNotSupported"},
		{"empty body", nil, "", nil, 400, "MissingRequestBodyError"},
		{"malformed body", nil, "<AccessControlPolicy>", nil, 400, "MalformedXML"},
		{"doctype body", nil, `<!DOCTYPE a><AccessControlPolicy/>`, nil, 400, "MalformedXML"},
		{"oversized body", nil, aclBody() + strings.Repeat(" ", 70<<10), nil, 400, "MaxMessageLengthExceeded"},
		{"payload opener failure", nil, aclBody(ownerGrant("butterdb")), errs.ErrSignatureMismatch, 403, "SignatureDoesNotMatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, fixtureOptions{bodiesErr: tc.bodiesErr})
			rec := call(f.handlers.PutBucketAcl, http.MethodPut, "/photos?acl", tc.body, tc.headers, routeFor(router.OpPutBucketAcl, "acl"), fullAuth())
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantCode != "" {
				if got := errorCode(t, rec.Body.String()); got != tc.wantCode {
					t.Fatalf("code = %s, want %s", got, tc.wantCode)
				}
			}
		})
	}
}

func TestPutBucketAclNeedsWriteAction(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	listOnly := authFor([]string{"photos"}, []string{"list"})
	rec := call(f.handlers.PutBucketAcl, http.MethodPut, "/photos?acl", "", map[string]string{"X-Amz-Acl": "private"}, routeFor(router.OpPutBucketAcl, "acl"), listOnly)
	if rec.Code != 403 {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestAnonymousNeverServed(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	for op, handler := range f.handlers.Routes() {
		t.Run(string(op), func(t *testing.T) {
			rec := call(handler, http.MethodGet, "/photos", "", nil, routeFor(op, "versioning"), anonymousAuth())
			if rec.Code != 403 {
				t.Fatalf("status = %d", rec.Code)
			}
		})
	}
}

func TestRoutesCoverBucketOperations(t *testing.T) {
	f := newFixture(t, fixtureOptions{})
	want := []router.Operation{
		router.OpHeadBucket, router.OpGetBucketLocation, router.OpCreateBucket, router.OpDeleteBucket,
		router.OpGetBucketPolicy, router.OpGetBucketPolicyStatus, router.OpGetBucketCors, router.OpGetBucketAcl,
		router.OpPutBucketAcl, router.OpGetBucketVersioning, router.OpGetBucketEncryption, router.OpGetBucketConfig,
	}
	routes := f.handlers.Routes()
	if len(routes) != len(want) {
		t.Fatalf("Routes has %d entries, want %d", len(routes), len(want))
	}
	for _, op := range want {
		if routes[op] == nil {
			t.Fatalf("no handler for %s", op)
		}
	}
}

func TestNewHandlersRejectsNilDependencies(t *testing.T) {
	valid := func() Dependencies {
		return Dependencies{
			Buckets:   fakeBuckets{},
			Evaluator: &fakeEvaluator{},
			Bodies:    fakeBodies{},
			Config:    Config{OwnerID: testOwnerID},
		}
	}
	cases := []struct {
		name   string
		mutate func(d *Dependencies)
	}{
		{"buckets", func(d *Dependencies) { d.Buckets = nil }},
		{"evaluator", func(d *Dependencies) { d.Evaluator = nil }},
		{"bodies", func(d *Dependencies) { d.Bodies = nil }},
		{"owner id", func(d *Dependencies) { d.Config.OwnerID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := valid()
			tc.mutate(&deps)
			if _, err := NewHandlers(deps); !errors.Is(err, errs.ErrMissingDependency) {
				t.Fatalf("NewHandlers error = %v", err)
			}
		})
	}
	if _, err := NewHandlers(valid()); err != nil {
		t.Fatalf("NewHandlers(valid): %v", err)
	}
}
