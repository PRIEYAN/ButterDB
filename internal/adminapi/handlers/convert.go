package handlers

import (
	"strconv"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/domain/access"
	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/object"
	"github.com/PRIEYAN/ButterDB/internal/identity"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

func utcMillis(t time.Time) time.Time {
	return t.UTC().Truncate(time.Millisecond)
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	utc := utcMillis(t)
	return &utc
}

func deviceDTO(d adminrepo.Device, currentID string) adminv1.Device {
	return adminv1.Device{
		DeviceID:      d.DeviceID,
		Name:          d.Name,
		Platform:      d.Platform,
		CreatedAt:     utcMillis(d.CreatedAt),
		CreatedVia:    string(d.CreatedVia),
		LastSeenAt:    utcMillis(d.LastSeenAt),
		LastSeenIP:    d.LastSeenIP,
		TokenIssuedAt: utcMillis(d.TokenIssuedAt),
		RevokedAt:     optionalTime(d.RevokedAt),
		Current:       d.DeviceID == currentID,
	}
}

func bucketDTO(info bucket.Info) adminv1.Bucket {
	return adminv1.Bucket{
		Name:               string(info.Name),
		CreatedAt:          time.Unix(info.CreatedDate, 0).UTC(),
		Policy:             info.Policy,
		EncryptionDefault:  info.DefaultEncryption,
		CacheMaxAgeSeconds: int(info.CacheMaxAge),
		ObjectCount:        info.ObjectCount,
		StoredBytes:        info.StoredBytes,
		TelegramChannelID:  info.ChannelID,
	}
}

func millisToOptionalTime(millis int64) *time.Time {
	if millis == 0 {
		return nil
	}
	t := time.UnixMilli(millis).UTC()
	return &t
}

func accessKeyDTO(info access.KeyInfo) adminv1.AccessKey {
	actions := make([]string, 0, len(info.Scope.Actions()))
	for _, action := range info.Scope.Actions() {
		actions = append(actions, action.String())
	}
	buckets := info.Scope.Buckets()
	if buckets == nil {
		buckets = []string{}
	}
	status := "active"
	if info.Revoked() {
		status = "revoked"
	}
	return adminv1.AccessKey{
		AccessKeyID: info.ID.String(),
		Name:        info.Name,
		CreatedAt:   time.UnixMilli(info.CreatedAt).UTC(),
		LastUsedAt:  millisToOptionalTime(info.LastUsedAt),
		Status:      status,
		RevokedAt:   millisToOptionalTime(info.RevokedAt),
		Scope:       adminv1.Scope{Buckets: buckets, Actions: actions},
	}
}

func objectSummaryDTO(meta object.ObjectMeta) adminv1.ObjectSummary {
	return adminv1.ObjectSummary{
		Key:          meta.Key,
		SizeBytes:    meta.PlaintextSize,
		ETag:         object.QuoteETag(meta.ETag),
		LastModified: time.Unix(meta.MsgDate, 0).UTC(),
		ContentType:  meta.ContentType,
		Encrypted:    meta.Encrypted,
	}
}

func objectMetaDTO(bucketName string, meta object.ObjectMeta) adminv1.ObjectMeta {
	userMeta := meta.UserMeta
	if userMeta == nil {
		userMeta = map[string]string{}
	}
	return adminv1.ObjectMeta{
		Bucket:       bucketName,
		Key:          meta.Key,
		VersionID:    strconv.FormatInt(meta.PutMsgID, 10),
		SizeBytes:    meta.PlaintextSize,
		StoredBytes:  meta.StoredSize,
		ETag:         object.QuoteETag(meta.ETag),
		SHA256:       meta.SHA256,
		ContentType:  meta.ContentType,
		LastModified: time.Unix(meta.MsgDate, 0).UTC(),
		Encrypted:    meta.Encrypted,
		UserMetadata: userMeta,
	}
}

func telegramStatusDTO(snap identity.Snapshot) adminv1.TelegramStatus {
	status := adminv1.TelegramStatus{
		State: adminv1.TelegramState(snap.State),
		Next:  adminv1.TelegramNext(snap.Next()),
		Credentials: adminv1.TelegramCredentials{
			APIID:      snap.Credentials.APIID,
			APIHashSet: snap.Credentials.APIHashSet,
		},
	}
	if snap.Login != nil {
		status.Login = telegramLoginDTO(*snap.Login)
	}
	if snap.Account != nil {
		status.Account = &adminv1.TelegramAccount{
			UserID:      snap.Account.UserID,
			Username:    snap.Account.Username,
			FirstName:   snap.Account.FirstName,
			PhoneMasked: snap.Account.PhoneMasked,
			LinkedAt:    utcMillis(snap.Account.LinkedAt),
			Connected:   snap.State == identity.StateLinked,
		}
	}
	if snap.LastError != nil {
		body := failureBody(*snap.LastError)
		status.LastError = &body
	}
	return status
}

func telegramLoginDTO(login identity.LoginSummary) *adminv1.TelegramLogin {
	return &adminv1.TelegramLogin{
		LoginID:     login.LoginID,
		PhoneMasked: login.PhoneMasked,
		CodeDelivery: adminv1.CodeDelivery{
			Type:           login.CodeDelivery.Type,
			Length:         login.CodeDelivery.Length,
			NextType:       login.CodeDelivery.NextType,
			TimeoutSeconds: login.CodeDelivery.TimeoutSeconds,
		},
		PasswordHint:         login.PasswordHint,
		CodeAttemptsLeft:     login.CodeAttemptsLeft,
		PasswordAttemptsLeft: login.PasswordAttemptsLeft,
		StartedAt:            utcMillis(login.StartedAt),
		ExpiresAt:            utcMillis(login.ExpiresAt),
	}
}

func failureBody(failure identity.Failure) adminv1.ErrorBody {
	details := map[string]any{}
	if failure.AttemptsLeft > 0 {
		details[telegramAttemptsLeftDetail] = failure.AttemptsLeft
	}
	if failure.BoundUserID != 0 {
		details["bound_user_id"] = failure.BoundUserID
	}
	if len(details) == 0 {
		details = nil
	}
	mapped := apiError(adminv1.Code(failure.Code), details)
	if failure.RetryAfter > 0 {
		mapped = withRetryAfter(mapped, failure.RetryAfter)
	}
	return mapped.Body
}
