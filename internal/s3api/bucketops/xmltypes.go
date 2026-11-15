package bucketops

import "encoding/xml"

type locationConstraint struct {
	XMLName xml.Name `xml:"LocationConstraint"`
	Value   string   `xml:",chardata"`
}

type policyStatus struct {
	XMLName  xml.Name `xml:"PolicyStatus"`
	IsPublic bool     `xml:"IsPublic"`
}

type corsRule struct {
	AllowedOrigins []string `xml:"AllowedOrigin"`
	AllowedMethods []string `xml:"AllowedMethod"`
	AllowedHeaders []string `xml:"AllowedHeader"`
	ExposeHeaders  []string `xml:"ExposeHeader"`
	MaxAgeSeconds  int      `xml:"MaxAgeSeconds"`
}

type corsConfiguration struct {
	XMLName xml.Name `xml:"CORSConfiguration"`
	Rule    corsRule `xml:"CORSRule"`
}

type versioningConfiguration struct {
	XMLName xml.Name `xml:"VersioningConfiguration"`
}

type sseDefault struct {
	Algorithm string `xml:"SSEAlgorithm"`
}

type sseRule struct {
	Default    sseDefault `xml:"ApplyServerSideEncryptionByDefault"`
	BucketKeys bool       `xml:"BucketKeyEnabled"`
}

type sseConfiguration struct {
	XMLName xml.Name `xml:"ServerSideEncryptionConfiguration"`
	Rule    sseRule  `xml:"Rule"`
}

type loggingStatus struct {
	XMLName xml.Name `xml:"BucketLoggingStatus"`
}

type requestPayment struct {
	XMLName xml.Name `xml:"RequestPaymentConfiguration"`
	Payer   string   `xml:"Payer"`
}

type ownershipRule struct {
	Ownership string `xml:"ObjectOwnership"`
}

type ownershipControls struct {
	XMLName xml.Name      `xml:"OwnershipControls"`
	Rule    ownershipRule `xml:"Rule"`
}

type notificationConfiguration struct {
	XMLName xml.Name `xml:"NotificationConfiguration"`
}

type accelerateConfiguration struct {
	XMLName xml.Name `xml:"AccelerateConfiguration"`
}
