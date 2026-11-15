package bucketops

import (
	"encoding/xml"
	"net/http"
	"slices"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/access/sigv4"
	"github.com/PRIEYAN/ButterDB/internal/s3api/router"
	"github.com/PRIEYAN/ButterDB/internal/s3api/s3err"
	"github.com/PRIEYAN/ButterDB/internal/s3api/xmlcodec"
)

const (
	xsiNamespace      = "http://www.w3.org/2001/XMLSchema-instance"
	aclHeader         = "X-Amz-Acl"
	grantHeaderPrefix = "X-Amz-Grant-"
	privateACL        = "private"
	aclNotAllowedText = "The bucket does not allow ACLs"
)

type aclOwner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName,omitempty"`
}

type aclGrantee struct {
	XMLName xml.Name `xml:"Grantee"`
	XSI     string   `xml:"xmlns:xsi,attr,omitempty"`
	Type    string   `xml:"xsi:type,attr,omitempty"`
	ID      string   `xml:"ID,omitempty"`
	Display string   `xml:"DisplayName,omitempty"`
	URI     string   `xml:"URI,omitempty"`
}

type aclGrant struct {
	Grantee    aclGrantee `xml:"Grantee"`
	Permission string     `xml:"Permission"`
}

type aclList struct {
	Grants []aclGrant `xml:"Grant"`
}

type accessControlPolicy struct {
	XMLName xml.Name `xml:"AccessControlPolicy"`
	Owner   aclOwner `xml:"Owner"`
	ACL     aclList  `xml:"AccessControlList"`
}

type incomingGrantee struct {
	Type string `xml:"http://www.w3.org/2001/XMLSchema-instance type,attr"`
	ID   string `xml:"ID"`
	URI  string `xml:"URI"`
}

type incomingGrant struct {
	Grantee    incomingGrantee `xml:"Grantee"`
	Permission string          `xml:"Permission"`
}

type incomingPolicy struct {
	XMLName xml.Name        `xml:"AccessControlPolicy"`
	Grants  []incomingGrant `xml:"AccessControlList>Grant"`
}

func (h *Handlers) ownerGrant() aclGrant {
	return aclGrant{
		Grantee: aclGrantee{
			XSI:     xsiNamespace,
			Type:    canonicalUserGrantee,
			ID:      h.deps.Config.OwnerID,
			Display: ownerDisplayName,
		},
		Permission: fullControl,
	}
}

func (h *Handlers) GetBucketAcl(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	if _, err := h.authorize(r, rt, a); err != nil {
		h.fail(w, r, rt, err)
		return
	}
	h.respond(w, r, rt, accessControlPolicy{
		Owner: aclOwner{ID: h.deps.Config.OwnerID, DisplayName: ownerDisplayName},
		ACL:   aclList{Grants: []aclGrant{h.ownerGrant()}},
	})
}

func (h *Handlers) PutBucketAcl(w http.ResponseWriter, r *http.Request, rt router.Route, a sigv4.Auth) {
	if _, err := h.authorize(r, rt, a); err != nil {
		h.fail(w, r, rt, err)
		return
	}
	if err := h.checkACLRequest(r, a); err != nil {
		h.fail(w, r, rt, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Handlers) checkACLRequest(r *http.Request, a sigv4.Auth) error {
	if hasGrantHeader(r.Header) {
		return s3err.New(s3err.CodeAccessControlListNotSupported, "")
	}
	canned := r.Header.Get(aclHeader)
	if canned != "" {
		return checkCannedBucketACL(canned)
	}
	return h.checkACLBody(r, a)
}

func hasGrantHeader(header http.Header) bool {
	for name := range header {
		if strings.HasPrefix(http.CanonicalHeaderKey(name), grantHeaderPrefix) {
			return true
		}
	}
	return false
}

func checkCannedBucketACL(canned string) error {
	switch canned {
	case privateACL:
		return nil
	case "public-read", "bucket-owner-full-control", "bucket-owner-read":
		return s3err.New(s3err.CodeAccessControlListNotSupported, "")
	}
	return s3err.New(s3err.CodeAccessControlListNotSupported, aclNotAllowedText)
}

func (h *Handlers) checkACLBody(r *http.Request, a sigv4.Auth) error {
	body, err := h.deps.Bodies.OpenBody(r, a)
	if err != nil {
		return err
	}
	defer body.Close()
	var incoming incomingPolicy
	if err := xmlcodec.Decode(body, aclBodyLimit, &incoming); err != nil {
		return err
	}
	if !slices.Equal(grantKeys(incoming.Grants), []string{h.ownerGrantKey()}) {
		return s3err.New(s3err.CodeAccessControlListNotSupported, "")
	}
	return nil
}

func (h *Handlers) ownerGrantKey() string {
	return grantKey(canonicalUserGrantee, h.deps.Config.OwnerID, fullControl)
}

func grantKeys(grants []incomingGrant) []string {
	keys := make([]string, 0, len(grants))
	for _, grant := range grants {
		identity := grant.Grantee.ID
		if grant.Grantee.Type == groupGrantee {
			identity = grant.Grantee.URI
		}
		keys = append(keys, grantKey(grant.Grantee.Type, identity, grant.Permission))
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

func grantKey(granteeType, identity, permission string) string {
	return granteeType + "|" + identity + "|" + permission
}
