package s3err

import (
	"errors"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
)

func MapSigV4(e *sigv4.Error) APIError {
	switch e.Reason {
	case sigv4.ReasonSignature:
		return signatureMismatch(e)
	case sigv4.ReasonUnknownKey:
		return withAccessKey(New(CodeInvalidAccessKeyId, ""), e)
	case sigv4.ReasonHeaderMalformed:
		return New(CodeAuthorizationHeaderMalformed, "")
	case sigv4.ReasonQueryMalformed:
		return New(CodeAuthorizationQueryParametersError, "")
	case sigv4.ReasonWrongRegion:
		return wrongRegion(e)
	case sigv4.ReasonSkew:
		return skewed(e)
	case sigv4.ReasonExpired:
		return expired(e)
	case sigv4.ReasonNotYetValid:
		return New(CodeAccessDenied, "Request is not valid yet")
	case sigv4.ReasonMissingDate:
		return New(CodeAccessDenied, "AWS authentication requires a valid Date or x-amz-date header")
	}
	return mapSigV4Payload(e)
}

func mapSigV4Payload(e *sigv4.Error) APIError {
	switch e.Reason {
	case sigv4.ReasonMissingContentSHA256:
		return New(CodeInvalidRequest, "Missing required header for this request: x-amz-content-sha256")
	case sigv4.ReasonBadContentSHA256:
		return New(CodeInvalidArgument, "x-amz-content-sha256 must be UNSIGNED-PAYLOAD, STREAMING-..., or a valid sha256 value.")
	case sigv4.ReasonContentSHA256Mismatch:
		return contentSHA256Mismatch(e)
	case sigv4.ReasonSecurityToken:
		return New(CodeInvalidToken, "")
	case sigv4.ReasonSigV2:
		return New(CodeInvalidRequest, "The authorization mechanism you have provided is not supported. Please use AWS4-HMAC-SHA256.")
	case sigv4.ReasonSigV4A:
		return New(CodeNotImplemented, "")
	case sigv4.ReasonBothMechanisms:
		return New(CodeInvalidArgument, "Only one auth mechanism allowed")
	case sigv4.ReasonUnsupportedScheme:
		return New(CodeInvalidArgument, "Unsupported Authorization Type")
	case sigv4.ReasonChunkFraming, sigv4.ReasonChecksumMalformed:
		return New(CodeInvalidRequest, "")
	case sigv4.ReasonIncompleteBody:
		return New(CodeIncompleteBody, "")
	case sigv4.ReasonChecksumMismatch:
		return checksumMismatch(e)
	}
	return fallbackSigV4(e)
}

func fallbackSigV4(e *sigv4.Error) APIError {
	if e.Err != nil {
		if mapped, ok := mapDomain(e.Err); ok {
			return mapped
		}
		var nested APIError
		if errors.As(e.Err, &nested) {
			return nested
		}
	}
	return New(CodeInternalError, "")
}

func withAccessKey(api APIError, e *sigv4.Error) APIError {
	if e.AccessKeyID != "" {
		api.Extra = append(api.Extra, Field{Name: "AWSAccessKeyId", Value: e.AccessKeyID})
	}
	return api
}

func signatureMismatch(e *sigv4.Error) APIError {
	api := withAccessKey(New(CodeSignatureDoesNotMatch, ""), e)
	if e.StringToSign != "" {
		api.Extra = append(api.Extra, Field{Name: "StringToSign", Value: e.StringToSign})
	}
	if e.CanonicalRequest != "" {
		api.Extra = append(api.Extra, Field{Name: "CanonicalRequest", Value: e.CanonicalRequest})
	}
	return api
}

func wrongRegion(e *sigv4.Error) APIError {
	return New(CodeAuthorizationHeaderMalformed, "the region '"+e.Region+"' is wrong")
}

func skewed(e *sigv4.Error) APIError {
	api := New(CodeRequestTimeTooSkewed, "")
	api.Extra = []Field{
		{Name: "RequestTime", Value: formatTime(e.RequestTime)},
		{Name: "ServerTime", Value: formatTime(e.ServerTime)},
		{Name: "MaxAllowedSkewMilliseconds", Value: maxAllowedSkewMillis},
	}
	return api
}

func expired(e *sigv4.Error) APIError {
	api := New(CodeAccessDenied, "Request has expired")
	api.Extra = []Field{
		{Name: "Expires", Value: formatTime(e.Expires)},
		{Name: "ServerTime", Value: formatTime(e.ServerTime)},
	}
	return api
}

func contentSHA256Mismatch(e *sigv4.Error) APIError {
	api := New(CodeXAmzContentSHA256Mismatch, "")
	api.Extra = []Field{
		{Name: "ClientComputedContentSHA256", Value: e.ClientSHA256},
		{Name: "S3ComputedContentSHA256", Value: e.ServerSHA256},
	}
	return api
}

func checksumMismatch(e *sigv4.Error) APIError {
	message := ""
	if e.Algorithm != "" {
		message = "The " + e.Algorithm + " you specified did not match the calculated checksum."
	}
	return New(CodeBadDigest, message)
}

func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}
