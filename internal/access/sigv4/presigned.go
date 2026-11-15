package sigv4

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const unsignedPayload = "UNSIGNED-PAYLOAD"

func (v *Verifier) authenticatePresigned(ctx context.Context, r *http.Request, params []queryParam) (Auth, error) {
	algorithm, _ := firstParam(params, queryAlgorithm)
	switch algorithm {
	case schemeSigV4:
	case schemeSigV4A:
		return Auth{}, newError(ReasonSigV4A)
	default:
		return Auth{}, newError(ReasonQueryMalformed)
	}
	credential, okCredential := firstParam(params, queryCredential)
	dateText, okDate := firstParam(params, queryDate)
	expiresText, okExpires := firstParam(params, queryExpires)
	signedText, okSigned := firstParam(params, querySignedHeaders)
	signature, okSignature := firstParam(params, querySignature)
	if !okCredential || !okDate || !okExpires || !okSigned || !okSignature {
		return Auth{}, newError(ReasonQueryMalformed)
	}
	scope, ok := parseCredential(credential)
	if !ok {
		return Auth{}, newError(ReasonQueryMalformed)
	}
	failure := func(reason Reason) *Error {
		e := newError(reason)
		e.AccessKeyID = scope.accessKeyID
		e.Region = scope.region
		e.Algorithm = signingAlgorithm
		return e
	}
	if !isLowerHex(signature, signatureHexLength) {
		return Auth{}, failure(ReasonSignature)
	}
	requestTime, err := time.Parse(timeLayout, dateText)
	if err != nil {
		return Auth{}, failure(ReasonQueryMalformed)
	}
	expires, ok := v.parseExpires(expiresText)
	if !ok {
		return Auth{}, failure(ReasonQueryMalformed)
	}
	signedHeaders, ok := parseSignedHeaders(signedText)
	if !ok || scope.date != requestTime.Format(dateLayout) {
		return Auth{}, failure(ReasonQueryMalformed)
	}
	if !v.regionServed(scope.region) {
		return Auth{}, failure(ReasonWrongRegion)
	}
	if e := v.presignTimeFailure(requestTime, expires, failure); e != nil {
		return Auth{}, e
	}
	hashedPayload, contentSHA256 := presignedPayload(r, params)
	payload, err := classifyPayload(r, contentSHA256, true)
	if err != nil {
		return Auth{}, failure(asReason(err))
	}
	secret, info, err := v.lookupKey(ctx, scope.accessKeyID)
	if err != nil {
		return Auth{}, v.lookupFailure(err, failure)
	}
	signingKey := v.keys.signingKey(signingKeyID{accessKeyID: scope.accessKeyID, date: scope.date, region: scope.region}, secret)
	canonical, present, err := buildCanonicalRequest(canonicalInput{
		request:       r,
		params:        params,
		skipParam:     querySignature,
		signedHeaders: signedHeaders,
		hashedPayload: hashedPayload,
	})
	if err != nil {
		return Auth{}, failure(ReasonQueryMalformed)
	}
	if !present {
		return Auth{}, failure(ReasonSignature)
	}
	stringToSign := requestStringToSign(requestTime, credentialScopeText(scope.date, scope.region), canonical)
	if !signaturesEqual(hexSignature(signingKey, stringToSign), signature) {
		e := failure(ReasonSignature)
		e.StringToSign, e.CanonicalRequest = stringToSign, canonical
		e.RequestTime, e.ServerTime = requestTime, v.cfg.Now()
		return Auth{}, e
	}
	return Auth{
		Kind:          AuthPresigned,
		Key:           info,
		Mode:          payload.mode,
		ContentSHA256: contentSHA256,
		DecodedLength: payload.decodedLength,
		Trailer:       payload.trailer,
		RequestTime:   requestTime,
		Region:        scope.region,
		signingKey:    signingKey,
	}, nil
}

func asReason(err error) Reason {
	if typed, ok := err.(*Error); ok {
		return typed.Reason
	}
	return ReasonQueryMalformed
}

func (v *Verifier) parseExpires(text string) (time.Duration, bool) {
	seconds, err := strconv.ParseInt(text, 10, 64)
	if err != nil || seconds < 1 || seconds > int64(v.cfg.MaxPresign/time.Second) {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

func (v *Verifier) presignTimeFailure(requestTime time.Time, expires time.Duration, failure func(Reason) *Error) *Error {
	now := v.cfg.Now()
	expiresAt := requestTime.Add(expires)
	if now.After(expiresAt) {
		e := failure(ReasonExpired)
		e.RequestTime, e.ServerTime, e.Expires = requestTime, now, expiresAt
		return e
	}
	if requestTime.After(now.Add(v.cfg.MaxSkew)) {
		e := failure(ReasonNotYetValid)
		e.RequestTime, e.ServerTime, e.Expires = requestTime, now, expiresAt
		return e
	}
	return nil
}

func presignedPayload(r *http.Request, params []queryParam) (hashedPayload string, contentSHA256 string) {
	if value, ok := firstParam(params, queryContentSHA256); ok {
		return value, value
	}
	if value := r.Header.Get(headerContentSHA256); value != "" {
		return unsignedPayload, value
	}
	return unsignedPayload, unsignedPayload
}

func Presign(in PresignInput) (string, error) {
	if err := validatePresignInput(in); err != nil {
		return "", err
	}
	signedAt := in.Time.UTC()
	date := signedAt.Format(dateLayout)
	params, err := decodeQuery(in.URL.RawQuery)
	if err != nil {
		return "", fmtPresignError("query is not valid percent encoding")
	}
	params = append(params,
		queryParam{name: queryAlgorithm, value: signingAlgorithm},
		queryParam{name: queryCredential, value: strings.Join([]string{in.AccessKeyID.String(), credentialScopeText(date, in.Region)}, "/")},
		queryParam{name: queryDate, value: signedAt.Format(timeLayout)},
		queryParam{name: queryExpires, value: strconv.FormatInt(int64(in.Expires/time.Second), 10)},
		queryParam{name: querySignedHeaders, value: "host"},
	)
	target := &http.Request{Method: in.Method, URL: in.URL, Host: in.URL.Host}
	canonical, _, err := buildCanonicalRequest(canonicalInput{
		request:       target,
		params:        params,
		signedHeaders: []string{"host"},
		hashedPayload: unsignedPayload,
	})
	if err != nil {
		return "", fmtPresignError("path is not valid percent encoding")
	}
	stringToSign := requestStringToSign(signedAt, credentialScopeText(date, in.Region), canonical)
	signature := hexSignature(deriveSigningKey(in.Secret, date, in.Region), stringToSign)
	path, err := canonicalURI(in.URL)
	if err != nil {
		return "", fmtPresignError("path is not valid percent encoding")
	}
	scheme := in.URL.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + in.URL.Host + path + "?" + canonicalQuery(params, "") + "&" + querySignature + "=" + signature, nil
}

func validatePresignInput(in PresignInput) error {
	switch {
	case in.URL == nil || in.URL.Host == "":
		return fmtPresignError("url host is required")
	case in.Method == "":
		return fmtPresignError("method is required")
	case in.AccessKeyID == "" || len(in.Secret) == 0:
		return fmtPresignError("credentials are required")
	case in.Region == "":
		return fmtPresignError("region is required")
	case in.Expires < time.Second || in.Expires > defaultMaxPresign:
		return fmtPresignError("expires must be 1 to 604800 seconds")
	case hasQueryName(in.URL, querySignature) || hasQueryName(in.URL, queryAlgorithm):
		return fmtPresignError("url already carries signing parameters")
	}
	return nil
}

func hasQueryName(u *url.URL, name string) bool {
	params, err := decodeQuery(u.RawQuery)
	return err == nil && hasParam(params, name)
}
