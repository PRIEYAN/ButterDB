package model

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"maps"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

type Presentation string

const (
	PresentInline Presentation = "inline"
	PresentBanner Presentation = "banner"
	PresentDialog Presentation = "dialog"
	PresentRoute  Presentation = "route"
)

type Problem struct {
	Code         adminv1.Code
	Title        string
	Body         string
	Hint         string
	Field        string
	Presentation Presentation
	Route        Route
	Retryable    bool
	RetryAt      time.Time
	RequestID    string
	Details      map[string]any
}

const detailHTTPStatus = "http_status"

func Describe(err error, now time.Time) Problem {
	if err == nil {
		return Problem{}
	}
	var apiErr *adminv1.APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		return describeAPI(apiErr, now)
	}
	if errors.Is(err, context.Canceled) {
		return Problem{}
	}
	if p, ok := describeEmbedded(err); ok {
		return p
	}
	if isCertificateError(err) {
		return Problem{Title: textCertificateUntrust, Presentation: PresentDialog, Details: errorDetails(err)}
	}
	if isTransportError(err) {
		return Problem{
			Title:        textUnreachable,
			Body:         textUnreachableBody,
			Presentation: PresentBanner,
			Retryable:    true,
			Details:      errorDetails(err),
		}
	}
	return Problem{Title: textUnexpected, Body: textUnexpectedBody, Presentation: PresentDialog, Details: errorDetails(err)}
}

func describeAPI(apiErr *adminv1.APIError, now time.Time) Problem {
	c := apiContext{err: apiErr, now: now}
	body := apiErr.Body
	p := Problem{
		Code:      body.Code,
		Hint:      body.Hint,
		Field:     c.text("field"),
		Retryable: body.Retryable,
		RequestID: body.RequestID,
		Details:   apiDetails(apiErr),
	}
	e, known := lookup(body.Code)
	if !known {
		return describeUnknownCode(p, body)
	}
	p.Presentation = e.presentation
	p.Route = e.route
	p.Title = e.title(c)
	if e.body != nil {
		p.Body = e.body(c)
	}
	if p.Field == "" {
		p.Field = e.field
	}
	if e.dialogWithoutField && c.text("field") == "" {
		p.Presentation = PresentDialog
		p.Field = ""
	}
	p.Retryable = p.Retryable || e.retryable
	p.RetryAt = retryAt(c)
	return p
}

func describeUnknownCode(p Problem, body adminv1.ErrorBody) Problem {
	p.Presentation = PresentDialog
	p.Title = body.Message
	if p.Title == "" {
		p.Title = textUnknownCode
	}
	p.Body = "Code: " + string(body.Code)
	return p
}

func retryAt(c apiContext) time.Time {
	if c.err.Body.Code == adminv1.CodeTelegramFloodWait {
		if until, ok := floodWaitUntil(c); ok {
			return until
		}
		return time.Time{}
	}
	if c.err.Body.RetryAfterSeconds > 0 {
		return c.now.Add(time.Duration(c.err.Body.RetryAfterSeconds) * time.Second)
	}
	return time.Time{}
}

func apiDetails(apiErr *adminv1.APIError) map[string]any {
	details := make(map[string]any, len(apiErr.Body.Details)+1)
	maps.Copy(details, apiErr.Body.Details)
	details[detailHTTPStatus] = apiErr.Status
	return details
}

func errorDetails(err error) map[string]any {
	return map[string]any{"error": err.Error()}
}

func isCertificateError(err error) bool {
	var verification *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var systemRoots x509.SystemRootsError
	return errors.As(err, &verification) ||
		errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostname) ||
		errors.As(err, &invalid) ||
		errors.As(err, &systemRoots)
}

func isTransportError(err error) bool {
	var netErr net.Error
	var urlErr *url.Error
	return errors.As(err, &netErr) ||
		errors.As(err, &urlErr) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, os.ErrDeadlineExceeded) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}
