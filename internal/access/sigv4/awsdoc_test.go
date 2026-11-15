package sigv4

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func checkDocExampleCanonical(t *testing.T, example docExample, req *http.Request, signedHeaders string, payloadHash string, skipSignature string) {
	t.Helper()
	params, err := decodeQuery(req.URL.RawQuery)
	if err != nil {
		t.Fatal(err)
	}
	canonical, present, err := buildCanonicalRequest(canonicalInput{
		request:       req,
		params:        params,
		skipParam:     skipSignature,
		signedHeaders: strings.Split(signedHeaders, ";"),
		hashedPayload: payloadHash,
	})
	if err != nil || !present {
		t.Fatalf("buildCanonicalRequest() present=%v err=%v", present, err)
	}
	if canonical != example.CanonicalRequest {
		t.Fatalf("canonical request mismatch\n got: %q\nwant: %q", canonical, example.CanonicalRequest)
	}
	now := example.env(t).clock.Now()
	scope := credentialScopeText(now.Format(dateLayout), example.Region)
	if got := requestStringToSign(now, scope, canonical); example.StringToSign != "" && got != example.StringToSign {
		t.Fatalf("string to sign mismatch\n got: %q\nwant: %q", got, example.StringToSign)
	}
}

func TestAWSDocExampleGetObject(t *testing.T) {
	runHeaderDocExample(t, "awsdoc_get_object.json", "host;range;x-amz-content-sha256;x-amz-date")
}

func TestAWSDocExamplePutObject(t *testing.T) {
	runHeaderDocExample(t, "awsdoc_put_object.json", "date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class")
}

func TestAWSDocExampleListObjects(t *testing.T) {
	runHeaderDocExample(t, "awsdoc_list_objects.json", "host;x-amz-content-sha256;x-amz-date")
}

func runHeaderDocExample(t *testing.T, file string, signedHeaders string) {
	t.Helper()
	example := loadDocExample(t, file)
	env := example.env(t)
	req := example.request(t)
	checkDocExampleCanonical(t, example, req, signedHeaders, req.Header.Get("X-Amz-Content-Sha256"), "")

	auth, err := env.verifier.Authenticate(context.Background(), req)
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if auth.Kind != AuthHeader || auth.Region != example.Region || string(auth.Key.ID) != example.AccessKeyID {
		t.Fatalf("Authenticate() = %+v", auth)
	}

	tampered := example.request(t)
	tampered.Header.Set("Authorization", strings.Replace(example.Authorization, example.Signature[:4], "0000", 1))
	_, err = env.verifier.Authenticate(context.Background(), tampered)
	typed := requireReason(t, err, ReasonSignature)
	if strings.Contains(typed.Error(), example.Signature) || strings.Contains(typed.Error(), "0000") {
		t.Fatalf("error text leaks the signature: %q", typed.Error())
	}
	if typed.CanonicalRequest != example.CanonicalRequest {
		t.Fatalf("Error.CanonicalRequest = %q", typed.CanonicalRequest)
	}
}
