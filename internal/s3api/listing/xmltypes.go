package listing

import "encoding/xml"

type ownerEntry struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

type bucketEntry struct {
	Name         string `xml:"Name"`
	CreationDate string `xml:"CreationDate"`
	BucketRegion string `xml:"BucketRegion"`
}

type bucketList struct {
	Buckets []bucketEntry `xml:"Bucket"`
}

type listAllMyBucketsResult struct {
	XMLName           xml.Name   `xml:"ListAllMyBucketsResult"`
	Owner             ownerEntry `xml:"Owner"`
	Buckets           bucketList `xml:"Buckets"`
	ContinuationToken string     `xml:"ContinuationToken,omitempty"`
	Prefix            string     `xml:"Prefix,omitempty"`
}

type contentsEntry struct {
	Key          string      `xml:"Key"`
	LastModified string      `xml:"LastModified"`
	ETag         string      `xml:"ETag"`
	Size         int64       `xml:"Size"`
	StorageClass string      `xml:"StorageClass"`
	Owner        *ownerEntry `xml:"Owner,omitempty"`
}

type prefixEntry struct {
	Prefix string `xml:"Prefix"`
}

type listBucketResultV2 struct {
	XMLName               xml.Name        `xml:"ListBucketResult"`
	Name                  string          `xml:"Name"`
	Prefix                string          `xml:"Prefix"`
	Delimiter             string          `xml:"Delimiter,omitempty"`
	MaxKeys               int             `xml:"MaxKeys"`
	KeyCount              int             `xml:"KeyCount"`
	IsTruncated           bool            `xml:"IsTruncated"`
	ContinuationToken     string          `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string          `xml:"NextContinuationToken,omitempty"`
	StartAfter            string          `xml:"StartAfter,omitempty"`
	EncodingType          string          `xml:"EncodingType,omitempty"`
	Contents              []contentsEntry `xml:"Contents"`
	CommonPrefixes        []prefixEntry   `xml:"CommonPrefixes"`
}

type listBucketResultV1 struct {
	XMLName        xml.Name        `xml:"ListBucketResult"`
	Name           string          `xml:"Name"`
	Prefix         string          `xml:"Prefix"`
	Marker         string          `xml:"Marker"`
	Delimiter      string          `xml:"Delimiter,omitempty"`
	MaxKeys        int             `xml:"MaxKeys"`
	IsTruncated    bool            `xml:"IsTruncated"`
	NextMarker     string          `xml:"NextMarker,omitempty"`
	EncodingType   string          `xml:"EncodingType,omitempty"`
	Contents       []contentsEntry `xml:"Contents"`
	CommonPrefixes []prefixEntry   `xml:"CommonPrefixes"`
}
