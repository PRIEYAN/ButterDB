package model

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

const (
	textVersionMismatch     = "The app and server disagree about this request. Update both to the same version."
	textNotOffered          = "The server does not offer this. It may be older than this app."
	textMisdirected         = "The server rejected this host name. Add it to admin.hostnames on the server."
	textPayloadTooLarge     = "The request is too large."
	textNotImplemented      = "This server does not support this feature yet."
	textNotSignedIn         = "Telegram signed this app out. Log in again."
	textRevoked             = "This device was revoked."
	textAlreadyClaimed      = "This engine is already set up."
	textNotClaimed          = "This engine is not set up yet."
	textSetupTokenInvalid   = "That setup token is wrong, used or expired."
	textPairingCodeInvalid  = "That code is wrong or expired."
	textPairingUnavailable  = "Telegram is not linked. Use butterdbd admin-token on the server."
	textLastDevice          = "This is the last active device."
	textLastDeviceBody      = "After revoking it, only butterdbd admin-token on the host can add a device."
	textDeviceNotFound      = "That device no longer exists."
	textTelegramNotLinked   = "Link a Telegram account first."
	textTelegramUnavailable = "The server cannot reach Telegram right now."
	textPhoneBanned         = "Telegram has banned this phone number."
	textBucketNotFound      = "That bucket no longer exists."
	textBucketExists        = "A bucket with this name exists."
	textAccessKeyNotFound   = "That access key no longer exists."
	textScopeInvalid        = "Choose at least one bucket and one permission."
	textObjectNotFound      = "That object no longer exists."
	textObjectKeyInvalid    = "Keys are 1 to 1024 bytes of UTF-8."
	textChecksumMismatch    = "The file changed while uploading. Retry."
	textPreconditionFailed  = "The object changed in the meantime. Choose again."
	textIdempotencyConflict = "This upload clashed with an earlier one. Retry to send it with a new key."
	textCommitUnknown       = "Checking whether Telegram saved it…"
	textPresignKeyInvalid   = "This key cannot read this bucket."
	textPublicURLUnset      = "The server has no public S3 URL configured. Set it in the server configuration (docs/spec/operations.md) and restart."
	textReplicaChanged      = "The replica state changed. Refreshed."
	textReplicaSameAccount  = "Use a different Telegram account from the primary."
	textCodeInvalid         = "That code is not right."
	textPasswordInvalid     = "Wrong password."
	textCodeExpired         = "The code expired. Request a new one."
	textAttemptsExhausted   = "Too many wrong attempts. Start again."
	textAPIIDInvalid        = "Telegram rejected this api_id and api_hash."
	textEmailSetupRequired  = "Telegram asks this account to set up a login email first. Do that in an official Telegram app, then try again."
	textCredentialsMissing  = "Enter the Telegram api_id and api_hash first."
	textAlreadyLinked       = "This server is already linked to a Telegram account."
	textLoginInProgress     = "A Telegram login is already in progress. Resuming it."
	textLoginNotActive      = "No Telegram login is active. Refreshed."
	textLoginFailed         = "Telegram could not complete the login."
	textCheckValue          = "Check this value"
	textUnknownCode         = "The server returned an error this app does not know."
	textUnreachable         = "The butterDB engine is not answering."
	textUnreachableBody     = "Close butterDB and open it again."
	textCertificateUntrust  = "The server certificate is not trusted by this computer."
	textUnexpected          = "Something went wrong."
	textUnexpectedBody      = "The app hit an unexpected error."
)

const (
	fieldName       = "name"
	fieldScope      = "scope"
	fieldKey        = "key"
	fieldMetadata   = "metadata"
	fieldSigningKey = "signing_key"
	fieldSetupToken = "setup_token"
	fieldPairCode   = "pairing_code"
	fieldPhone      = "phone"
	fieldCode       = "code"
	fieldPassword   = "password"
	fieldAPIID      = "api_id"
)

type apiContext struct {
	err *adminv1.APIError
	now time.Time
}

func (c apiContext) text(key string) string {
	switch v := c.err.Body.Details[key].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func (c apiContext) number(key string) (int64, bool) {
	switch v := c.err.Body.Details[key].(type) {
	case float64:
		return int64(v), true
	case int:
		return int64(v), true
	case int64:
		return v, true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	}
	return 0, false
}

func (c apiContext) instant(key string) (time.Time, bool) {
	switch v := c.err.Body.Details[key].(type) {
	case string:
		t, err := time.Parse(time.RFC3339Nano, v)
		return t, err == nil
	case time.Time:
		return v, !v.IsZero()
	case float64:
		return time.Unix(int64(v), 0), true
	}
	return time.Time{}, false
}

func (c apiContext) location() *time.Location {
	return c.now.Location()
}

type entry struct {
	presentation       Presentation
	dialogWithoutField bool
	route              Route
	field              string
	retryable          bool
	title              func(apiContext) string
	body               func(apiContext) string
}

func constant(s string) func(apiContext) string {
	return func(apiContext) string { return s }
}

func fixed(p Presentation, title string) entry {
	return entry{presentation: p, title: constant(title)}
}

func routed(r Route, title string) entry {
	return entry{presentation: PresentRoute, route: r, title: constant(title)}
}

func (e entry) onField(field string) entry {
	e.field = field
	return e
}

func (e entry) retry() entry {
	e.retryable = true
	return e
}

func (e entry) withBody(s string) entry {
	e.body = constant(s)
	return e
}

func (e entry) withBodyFunc(fn func(apiContext) string) entry {
	e.body = fn
	return e
}

func (e entry) withTitleFunc(fn func(apiContext) string) entry {
	e.title = fn
	return e
}

func attemptsLeft(base string) func(apiContext) string {
	return func(c apiContext) string {
		n, ok := c.number("attempts_left")
		if !ok {
			return base
		}
		return base + " " + strconv.FormatInt(n, 10) + " attempts left."
	}
}

func lookup(code adminv1.Code) (entry, bool) {
	switch code {
	case adminv1.CodeBadRequest, adminv1.CodeLengthRequired, adminv1.CodeUnsupportedMediaType, adminv1.CodeRangeNotSatisfiable:
		return fixed(PresentDialog, textVersionMismatch), true
	case adminv1.CodeValidationFailed:
		return validationFailed(), true
	case adminv1.CodeUnauthenticated:
		return routed(RouteTelegram, textNotSignedIn), true
	case adminv1.CodeDeviceRevoked:
		return fixed(PresentDialog, textRevoked).withBodyFunc(revokedBody), true
	case adminv1.CodeNotFound, adminv1.CodeMethodNotAllowed:
		return fixed(PresentDialog, textNotOffered), true
	case adminv1.CodeMisdirectedRequest:
		return fixed(PresentDialog, textMisdirected), true
	case adminv1.CodePayloadTooLarge:
		return fixed(PresentDialog, textPayloadTooLarge), true
	case adminv1.CodeRateLimited:
		return fixed(PresentBanner, "").withTitleFunc(rateLimitedTitle).retry(), true
	case adminv1.CodeInternal:
		return fixed(PresentDialog, "").withTitleFunc(internalTitle), true
	case adminv1.CodeServerNotReady:
		return fixed(PresentBanner, "").withTitleFunc(notReadyTitle).retry(), true
	case adminv1.CodeNotImplemented:
		return fixed(PresentInline, textNotImplemented), true
	case adminv1.CodeAlreadyClaimed:
		return fixed(PresentDialog, textAlreadyClaimed), true
	case adminv1.CodeNotClaimed:
		return fixed(PresentDialog, textNotClaimed), true
	case adminv1.CodeSetupTokenInvalid:
		return fixed(PresentInline, textSetupTokenInvalid).onField(fieldSetupToken), true
	case adminv1.CodePairingCodeInvalid:
		return fixed(PresentInline, textPairingCodeInvalid).onField(fieldPairCode), true
	case adminv1.CodePairingUnavailable:
		return fixed(PresentInline, textPairingUnavailable), true
	case adminv1.CodeLastDevice:
		return fixed(PresentDialog, textLastDevice).withBody(textLastDeviceBody), true
	case adminv1.CodeDeviceNotFound:
		return fixed(PresentBanner, textDeviceNotFound), true
	case adminv1.CodeTelegramNotLinked:
		return routed(RouteTelegram, textTelegramNotLinked), true
	}
	return lookupTelegram(code)
}

func lookupTelegram(code adminv1.Code) (entry, bool) {
	switch code {
	case adminv1.CodeTelegramUnavailable:
		return fixed(PresentBanner, textTelegramUnavailable).retry(), true
	case adminv1.CodeTelegramFloodWait:
		return fixed(PresentBanner, "").withTitleFunc(floodWaitTitle).retry(), true
	case adminv1.CodeTelegramCredentialsMissing:
		return fixed(PresentInline, textCredentialsMissing).onField(fieldAPIID), true
	case adminv1.CodeTelegramAlreadyLinked:
		return fixed(PresentDialog, textAlreadyLinked), true
	case adminv1.CodeTelegramLoginInProgress:
		return fixed(PresentBanner, textLoginInProgress), true
	case adminv1.CodeTelegramLoginNotActive:
		return fixed(PresentBanner, textLoginNotActive), true
	case adminv1.CodeTelegramAPIIDInvalid:
		return fixed(PresentInline, textAPIIDInvalid).onField(fieldAPIID), true
	case adminv1.CodeTelegramPhoneInvalid, adminv1.CodeTelegramAccountNotRegistered:
		return fixed(PresentInline, "").withTitleFunc(phoneProblemTitle(code)).onField(fieldPhone), true
	case adminv1.CodeTelegramPhoneBanned:
		return fixed(PresentDialog, textPhoneBanned), true
	case adminv1.CodeTelegramCodeInvalid:
		return fixed(PresentInline, "").withTitleFunc(attemptsLeft(textCodeInvalid)).onField(fieldCode), true
	case adminv1.CodeTelegramCodeExpired:
		return fixed(PresentInline, textCodeExpired).onField(fieldPhone), true
	case adminv1.CodeTelegramPasswordInvalid:
		return fixed(PresentInline, "").withTitleFunc(attemptsLeft(textPasswordInvalid)).onField(fieldPassword), true
	case adminv1.CodeTelegramAttemptsExhausted:
		return fixed(PresentInline, textAttemptsExhausted).onField(fieldPhone), true
	case adminv1.CodeTelegramEmailSetupRequired:
		return fixed(PresentDialog, textEmailSetupRequired), true
	case adminv1.CodeTelegramAccountMismatch:
		return fixed(PresentDialog, "").withTitleFunc(accountMismatchTitle), true
	case adminv1.CodeTelegramLoginFailed:
		return fixed(PresentDialog, textLoginFailed).withBodyFunc(loginFailedBody), true
	}
	return lookupResources(code)
}

func lookupResources(code adminv1.Code) (entry, bool) {
	switch code {
	case adminv1.CodeBucketNotFound:
		return fixed(PresentBanner, textBucketNotFound), true
	case adminv1.CodeBucketExists:
		return fixed(PresentInline, textBucketExists).onField(fieldName), true
	case adminv1.CodeBucketNameInvalid:
		return fixed(PresentInline, "").withTitleFunc(reasonTitle).onField(fieldName), true
	case adminv1.CodeBucketNotEmpty:
		return fixed(PresentDialog, "").withTitleFunc(bucketNotEmptyTitle), true
	case adminv1.CodeBucketLimitReached:
		return fixed(PresentDialog, "").withTitleFunc(bucketLimitTitle), true
	case adminv1.CodeAccessKeyNotFound:
		return fixed(PresentBanner, textAccessKeyNotFound), true
	case adminv1.CodeScopeInvalid:
		return fixed(PresentInline, textScopeInvalid).onField(fieldScope), true
	case adminv1.CodeObjectNotFound:
		return fixed(PresentBanner, textObjectNotFound), true
	case adminv1.CodeObjectKeyInvalid:
		return fixed(PresentInline, textObjectKeyInvalid).onField(fieldKey), true
	case adminv1.CodeObjectTooLarge:
		return fixed(PresentInline, "").withTitleFunc(objectTooLargeTitle), true
	case adminv1.CodeMetadataTooLarge:
		return fixed(PresentInline, "").withTitleFunc(metadataTooLargeTitle).onField(fieldMetadata), true
	case adminv1.CodeChecksumMismatch:
		return fixed(PresentInline, textChecksumMismatch), true
	case adminv1.CodePreconditionFailed:
		return fixed(PresentInline, textPreconditionFailed), true
	case adminv1.CodeIdempotencyConflict:
		return fixed(PresentInline, textIdempotencyConflict), true
	case adminv1.CodeCommitOutcomeUnknown:
		return fixed(PresentInline, textCommitUnknown).retry(), true
	case adminv1.CodePresignKeyInvalid:
		return fixed(PresentInline, textPresignKeyInvalid).onField(fieldSigningKey), true
	case adminv1.CodeS3PublicURLUnset:
		return fixed(PresentDialog, textPublicURLUnset), true
	case adminv1.CodeReplicaNotLinked, adminv1.CodeReplicaAlreadyLinked:
		return fixed(PresentBanner, textReplicaChanged), true
	case adminv1.CodeReplicaSameAccount:
		return fixed(PresentInline, textReplicaSameAccount), true
	}
	return entry{}, false
}

func validationFailed() entry {
	e := fixed(PresentInline, "").withTitleFunc(reasonTitle)
	e.dialogWithoutField = true
	return e
}

func reasonTitle(c apiContext) string {
	reason := c.text("reason")
	if reason == "" {
		return textCheckValue + "."
	}
	return textCheckValue + ": " + reason
}

func revokedBody(c apiContext) string {
	at := c.text("revoked_at")
	if at == "" {
		return ""
	}
	return "This device was revoked at " + at + "."
}

func rateLimitedTitle(c apiContext) string {
	if c.err.Body.RetryAfterSeconds <= 0 {
		return "Too many attempts. Try again shortly."
	}
	return "Too many attempts. Try again in " + strconv.Itoa(c.err.Body.RetryAfterSeconds) + " s."
}

func internalTitle(c apiContext) string {
	if c.err.Body.RequestID == "" {
		return "The server hit an error. Check its log."
	}
	return "The server hit an error. Check its log for request " + c.err.Body.RequestID + "."
}

func notReadyTitle(c apiContext) string {
	state := c.text("state")
	if state == "" {
		state = "not ready"
	}
	return "The server is " + state + ". Waiting…"
}

func floodWaitTitle(c apiContext) string {
	until, ok := floodWaitUntil(c)
	if !ok {
		return "Telegram asked the server to wait before the next request."
	}
	return "Telegram asked the server to wait until " + until.In(c.location()).Format("15:04:05") + "."
}

func floodWaitUntil(c apiContext) (time.Time, bool) {
	if until, ok := c.instant("until"); ok {
		return until, true
	}
	if c.err.Body.RetryAfterSeconds > 0 {
		return c.now.Add(time.Duration(c.err.Body.RetryAfterSeconds) * time.Second), true
	}
	return time.Time{}, false
}

func phoneProblemTitle(code adminv1.Code) func(apiContext) string {
	return func(apiContext) string {
		if code == adminv1.CodeTelegramAccountNotRegistered {
			return "This phone number has no Telegram account."
		}
		return "Telegram does not accept this phone number."
	}
}

func accountMismatchTitle(c apiContext) string {
	id := c.text("bound_user_id")
	if id == "" {
		return "This server is bound to another Telegram user. Log in with that account."
	}
	return "This server is bound to Telegram user " + id + ". Log in with that account."
}

func loginFailedBody(c apiContext) string {
	reported := c.text("telegram_error")
	if reported == "" {
		return ""
	}
	return "Telegram reported: " + reported
}

func bucketNotEmptyTitle(c apiContext) string {
	n, ok := c.number("object_count")
	if !ok {
		return "This bucket still holds objects. Delete them first."
	}
	return strconv.FormatInt(n, 10) + " objects remain. Delete them first."
}

func bucketLimitTitle(c apiContext) string {
	n, ok := c.number("limit")
	if !ok {
		return "This server has reached its bucket limit."
	}
	return "This server allows at most " + strconv.FormatInt(n, 10) + " buckets."
}

func objectTooLargeTitle(c apiContext) string {
	n, ok := c.number("max_bytes")
	if !ok {
		return "Too large."
	}
	return "Too large (max " + formatBytes(n) + ")."
}

func metadataTooLargeTitle(c apiContext) string {
	n, ok := c.number("max_bytes")
	if !ok {
		return "Metadata is too large."
	}
	return "Metadata is limited to " + strconv.FormatInt(n, 10) + " bytes."
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		if n < 0 {
			n = 0
		}
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 5; m /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64) + " " + string("KMGTPE"[exp]) + "iB"
}
