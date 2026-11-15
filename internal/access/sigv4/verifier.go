package sigv4

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
)

const (
	schemeSigV4  = "AWS4-HMAC-SHA256"
	schemeSigV4A = "AWS4-ECDSA-P256-SHA256"
	schemeSigV2  = "AWS"

	headerAuthorization = "Authorization"
	headerSecurityToken = "X-Amz-Security-Token"
	headerAmzDate       = "X-Amz-Date"
	headerContentSHA256 = "X-Amz-Content-Sha256"
	headerDecodedLength = "X-Amz-Decoded-Content-Length"
	headerTrailer       = "X-Amz-Trailer"

	queryAlgorithm     = "X-Amz-Algorithm"
	queryCredential    = "X-Amz-Credential"
	queryDate          = "X-Amz-Date"
	queryExpires       = "X-Amz-Expires"
	querySignedHeaders = "X-Amz-SignedHeaders"
	querySignature     = "X-Amz-Signature"
	queryContentSHA256 = "X-Amz-Content-Sha256"
	querySecurityToken = "X-Amz-Security-Token"
	querySigV2KeyID    = "AWSAccessKeyId"
	querySigV2Sig      = "Signature"
)

type Verifier struct {
	cfg     Config
	secrets SecretSource
	keys    *signingKeyCache
}

func NewVerifier(cfg Config, secrets SecretSource) *Verifier {
	if secrets == nil {
		panic(fmt.Errorf("sigv4: %w: secrets", errs.ErrMissingDependency))
	}
	if cfg.Now == nil {
		panic(fmt.Errorf("sigv4: %w: clock", errs.ErrMissingDependency))
	}
	if cfg.Region == "" {
		cfg.Region = defaultRegion
	}
	if cfg.MaxSkew <= 0 {
		cfg.MaxSkew = defaultMaxSkew
	}
	if cfg.MaxPresign <= 0 {
		cfg.MaxPresign = defaultMaxPresign
	}
	return &Verifier{cfg: cfg, secrets: secrets, keys: newSigningKeyCache(cfg.Now)}
}

type credentialScope struct {
	accessKeyID string
	date        string
	region      string
}

func parseCredential(value string) (credentialScope, bool) {
	parts := strings.Split(value, "/")
	if len(parts) != 5 || parts[0] == "" || parts[3] != scopeService || parts[4] != scopeTerminator {
		return credentialScope{}, false
	}
	if _, err := time.Parse(dateLayout, parts[1]); err != nil || len(parts[1]) != len(dateLayout) {
		return credentialScope{}, false
	}
	if parts[2] == "" {
		return credentialScope{}, false
	}
	return credentialScope{accessKeyID: parts[0], date: parts[1], region: parts[2]}, true
}

func (v *Verifier) Authenticate(ctx context.Context, r *http.Request) (Auth, error) {
	authorization := r.Header.Values(headerAuthorization)
	params, queryErr := decodeQuery(r.URL.RawQuery)
	presigned := queryErr == nil && hasParam(params, queryAlgorithm)
	if len(authorization) > 0 && presigned {
		return Auth{}, newError(ReasonBothMechanisms)
	}
	if len(r.Header.Values(headerSecurityToken)) > 0 || hasParam(params, querySecurityToken) {
		return Auth{}, newError(ReasonSecurityToken)
	}
	switch {
	case len(authorization) > 0:
		return v.authenticateHeader(ctx, r, authorization[0], params, queryErr)
	case presigned:
		return v.authenticatePresigned(ctx, r, params)
	case hasParam(params, querySigV2KeyID) && hasParam(params, querySigV2Sig):
		return Auth{}, newError(ReasonSigV2)
	}
	return Auth{Kind: AuthAnonymous, Mode: PayloadNone, DecodedLength: r.ContentLength}, nil
}

func hasParam(params []queryParam, name string) bool {
	_, ok := firstParam(params, name)
	return ok
}

func firstParam(params []queryParam, name string) (string, bool) {
	for _, param := range params {
		if param.name == name {
			return param.value, true
		}
	}
	return "", false
}

type authorizationParts struct {
	credential    string
	signedHeaders string
	signature     string
}

func parseAuthorization(value string) (authorizationParts, bool) {
	rest := strings.TrimLeft(strings.TrimPrefix(value, schemeSigV4), " ")
	var parts authorizationParts
	seen := 0
	for _, component := range strings.Split(rest, ",") {
		name, content, ok := strings.Cut(strings.TrimSpace(component), "=")
		if !ok {
			return authorizationParts{}, false
		}
		target := authorizationTarget(&parts, name)
		if target == nil || *target != "" || content == "" {
			return authorizationParts{}, false
		}
		*target = content
		seen++
	}
	return parts, seen == 3
}

func authorizationTarget(parts *authorizationParts, name string) *string {
	switch name {
	case "Credential":
		return &parts.credential
	case "SignedHeaders":
		return &parts.signedHeaders
	case "Signature":
		return &parts.signature
	default:
		return nil
	}
}

func parseRequestTime(r *http.Request) (time.Time, bool) {
	if value := r.Header.Get(headerAmzDate); value != "" {
		parsed, err := time.Parse(timeLayout, value)
		return parsed, err == nil
	}
	if value := r.Header.Get("Date"); value != "" {
		parsed, err := http.ParseTime(value)
		return parsed.UTC(), err == nil
	}
	return time.Time{}, false
}

func (v *Verifier) authenticateHeader(ctx context.Context, r *http.Request, authorization string, params []queryParam, queryErr error) (Auth, error) {
	scheme, _, _ := strings.Cut(authorization, " ")
	switch scheme {
	case schemeSigV4:
	case schemeSigV4A:
		return Auth{}, newError(ReasonSigV4A)
	case schemeSigV2:
		return Auth{}, newError(ReasonSigV2)
	default:
		return Auth{}, newError(ReasonUnsupportedScheme)
	}
	if queryErr != nil {
		return Auth{}, newError(ReasonHeaderMalformed)
	}
	parts, ok := parseAuthorization(authorization)
	if !ok {
		return Auth{}, newError(ReasonHeaderMalformed)
	}
	scope, ok := parseCredential(parts.credential)
	if !ok {
		return Auth{}, newError(ReasonHeaderMalformed)
	}
	failure := func(reason Reason) *Error {
		e := newError(reason)
		e.AccessKeyID = scope.accessKeyID
		e.Region = scope.region
		e.Algorithm = signingAlgorithm
		return e
	}
	if !isLowerHex(parts.signature, signatureHexLength) {
		return Auth{}, failure(ReasonSignature)
	}
	requestTime, ok := parseRequestTime(r)
	if !ok {
		return Auth{}, failure(ReasonMissingDate)
	}
	contentSHA256 := r.Header.Get(headerContentSHA256)
	if contentSHA256 == "" {
		return Auth{}, failure(ReasonMissingContentSHA256)
	}
	signedHeaders, ok := parseSignedHeaders(parts.signedHeaders)
	if !ok || scope.date != requestTime.Format(dateLayout) {
		return Auth{}, failure(ReasonHeaderMalformed)
	}
	if !v.regionServed(scope.region) {
		return Auth{}, failure(ReasonWrongRegion)
	}
	now := v.cfg.Now()
	if skew := now.Sub(requestTime); skew > v.cfg.MaxSkew || -skew > v.cfg.MaxSkew {
		e := failure(ReasonSkew)
		e.RequestTime, e.ServerTime = requestTime, now
		return Auth{}, e
	}
	payload, err := classifyPayload(r, contentSHA256, false)
	if err != nil {
		var typed *Error
		if errors.As(err, &typed) {
			e := failure(typed.Reason)
			e.ClientSHA256, e.ServerSHA256 = typed.ClientSHA256, typed.ServerSHA256
			return Auth{}, e
		}
		return Auth{}, err
	}
	secret, info, err := v.lookupKey(ctx, scope.accessKeyID)
	if err != nil {
		return Auth{}, v.lookupFailure(err, failure)
	}
	signingKey := v.keys.signingKey(signingKeyID{accessKeyID: scope.accessKeyID, date: scope.date, region: scope.region}, secret)
	canonical, present, err := buildCanonicalRequest(canonicalInput{
		request:       r,
		params:        params,
		signedHeaders: signedHeaders,
		hashedPayload: contentSHA256,
	})
	if err != nil {
		return Auth{}, failure(ReasonHeaderMalformed)
	}
	if !present {
		return Auth{}, failure(ReasonSignature)
	}
	stringToSign := requestStringToSign(requestTime, credentialScopeText(scope.date, scope.region), canonical)
	if !signaturesEqual(hexSignature(signingKey, stringToSign), parts.signature) {
		e := failure(ReasonSignature)
		e.StringToSign, e.CanonicalRequest = stringToSign, canonical
		e.RequestTime, e.ServerTime = requestTime, now
		return Auth{}, e
	}
	return Auth{
		Kind:          AuthHeader,
		Key:           info,
		Mode:          payload.mode,
		ContentSHA256: contentSHA256,
		DecodedLength: payload.decodedLength,
		Trailer:       payload.trailer,
		RequestTime:   requestTime,
		Region:        scope.region,
		seed:          parts.signature,
		signingKey:    signingKey,
	}, nil
}

func (v *Verifier) regionServed(region string) bool {
	return region == v.cfg.Region || region == defaultRegion
}

func (v *Verifier) lookupKey(ctx context.Context, accessKeyID string) ([]byte, access.KeyInfo, error) {
	secret, info, err := v.secrets.Secret(ctx, access.AccessKeyID(accessKeyID))
	if err != nil {
		return nil, access.KeyInfo{}, err
	}
	if info.Revoked() || len(secret) == 0 {
		return nil, access.KeyInfo{}, errs.ErrAccessKeyRevoked
	}
	return secret, info, nil
}

func (v *Verifier) lookupFailure(err error, failure func(Reason) *Error) error {
	switch {
	case errors.Is(err, errs.ErrUnknownAccessKey),
		errors.Is(err, errs.ErrAccessKeyNotFound),
		errors.Is(err, errs.ErrAccessKeyRevoked),
		errors.Is(err, access.ErrInvalidAccessKeyID):
		return failure(ReasonUnknownKey)
	default:
		return fmt.Errorf("sigv4: secret lookup: %w", err)
	}
}

type payloadClass struct {
	mode          PayloadMode
	decodedLength int64
	trailer       string
}

func classifyPayload(r *http.Request, value string, presigned bool) (payloadClass, error) {
	mode, err := payloadModeOf(value, presigned)
	if err != nil {
		return payloadClass{}, err
	}
	class := payloadClass{mode: mode, decodedLength: r.ContentLength}
	if !isStreaming(mode) {
		if len(r.Header.Values(headerTrailer)) > 0 && mode != PayloadNone {
			return payloadClass{}, newError(ReasonChunkFraming)
		}
		return nonStreamingClass(r, class, value)
	}
	class.decodedLength = -1
	if raw := r.Header.Get(headerDecodedLength); raw != "" {
		length, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || length < 0 {
			return payloadClass{}, newError(ReasonChunkFraming)
		}
		class.decodedLength = length
	}
	if hasTrailer(mode) {
		class.trailer, err = trailerName(r)
		return class, err
	}
	if len(r.Header.Values(headerTrailer)) > 0 {
		return payloadClass{}, newError(ReasonChunkFraming)
	}
	return class, nil
}

func nonStreamingClass(r *http.Request, class payloadClass, value string) (payloadClass, error) {
	if class.mode != PayloadSignedHex || !bodylessMethod(r.Method) {
		return class, nil
	}
	if value != emptyPayloadSHA256 {
		e := newError(ReasonContentSHA256Mismatch)
		e.ClientSHA256, e.ServerSHA256 = value, emptyPayloadSHA256
		return payloadClass{}, e
	}
	return class, nil
}

func bodylessMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodDelete
}

func isStreaming(mode PayloadMode) bool {
	return mode == PayloadStreamingSigned || mode == PayloadStreamingSignedTrailer || mode == PayloadStreamingUnsignedTrailer
}

func hasTrailer(mode PayloadMode) bool {
	return mode == PayloadStreamingSignedTrailer || mode == PayloadStreamingUnsignedTrailer
}

func payloadModeOf(value string, presigned bool) (PayloadMode, error) {
	switch value {
	case "UNSIGNED-PAYLOAD":
		return PayloadUnsigned, nil
	case "STREAMING-UNSIGNED-PAYLOAD-TRAILER":
		return PayloadStreamingUnsignedTrailer, nil
	case "STREAMING-AWS4-HMAC-SHA256-PAYLOAD":
		return signedStreamingMode(PayloadStreamingSigned, presigned)
	case "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER":
		return signedStreamingMode(PayloadStreamingSignedTrailer, presigned)
	case "STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD", "STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD-TRAILER":
		return PayloadNone, newError(ReasonSigV4A)
	}
	if isLowerHex(value, signatureHexLength) {
		return PayloadSignedHex, nil
	}
	return PayloadNone, newError(ReasonBadContentSHA256)
}

func signedStreamingMode(mode PayloadMode, presigned bool) (PayloadMode, error) {
	if presigned {
		return PayloadNone, newError(ReasonBadContentSHA256)
	}
	return mode, nil
}

func trailerName(r *http.Request) (string, error) {
	values := r.Header.Values(headerTrailer)
	if len(values) != 1 {
		return "", newError(ReasonChunkFraming)
	}
	name := strings.ToLower(strings.TrimSpace(values[0]))
	if _, ok := checksumAlgorithmOfTrailer(name); !ok {
		return "", newError(ReasonChecksumMalformed)
	}
	return name, nil
}
