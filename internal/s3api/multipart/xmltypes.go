package multipart

import "encoding/xml"

type initiateResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

type copyPartResult struct {
	XMLName      xml.Name `xml:"CopyPartResult"`
	ETag         string   `xml:"ETag"`
	LastModified string   `xml:"LastModified"`
}

type completeResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	Location string   `xml:"Location"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
}

type completePart struct {
	PartNumber        int    `xml:"PartNumber"`
	ETag              string `xml:"ETag"`
	ChecksumCRC32     string `xml:"ChecksumCRC32"`
	ChecksumCRC32C    string `xml:"ChecksumCRC32C"`
	ChecksumCRC64NVME string `xml:"ChecksumCRC64NVME"`
	ChecksumSHA1      string `xml:"ChecksumSHA1"`
	ChecksumSHA256    string `xml:"ChecksumSHA256"`
}

type completeRequest struct {
	XMLName xml.Name       `xml:"CompleteMultipartUpload"`
	Parts   []completePart `xml:"Part"`
}

type identity struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

type listedPart struct {
	PartNumber        int    `xml:"PartNumber"`
	LastModified      string `xml:"LastModified"`
	ETag              string `xml:"ETag"`
	Size              int64  `xml:"Size"`
	ChecksumCRC32     string `xml:"ChecksumCRC32,omitempty"`
	ChecksumCRC32C    string `xml:"ChecksumCRC32C,omitempty"`
	ChecksumCRC64NVME string `xml:"ChecksumCRC64NVME,omitempty"`
	ChecksumSHA1      string `xml:"ChecksumSHA1,omitempty"`
	ChecksumSHA256    string `xml:"ChecksumSHA256,omitempty"`
}

type listPartsResult struct {
	XMLName              xml.Name     `xml:"ListPartsResult"`
	Bucket               string       `xml:"Bucket"`
	Key                  string       `xml:"Key"`
	UploadID             string       `xml:"UploadId"`
	PartNumberMarker     int          `xml:"PartNumberMarker"`
	NextPartNumberMarker int          `xml:"NextPartNumberMarker,omitempty"`
	MaxParts             int          `xml:"MaxParts"`
	IsTruncated          bool         `xml:"IsTruncated"`
	Parts                []listedPart `xml:"Part"`
	Initiator            identity     `xml:"Initiator"`
	Owner                identity     `xml:"Owner"`
	StorageClass         string       `xml:"StorageClass"`
	ChecksumAlgorithm    string       `xml:"ChecksumAlgorithm,omitempty"`
	ChecksumType         string       `xml:"ChecksumType,omitempty"`
}

type listedUpload struct {
	Key          string   `xml:"Key"`
	UploadID     string   `xml:"UploadId"`
	Initiator    identity `xml:"Initiator"`
	Owner        identity `xml:"Owner"`
	StorageClass string   `xml:"StorageClass"`
	Initiated    string   `xml:"Initiated"`
}

type commonPrefix struct {
	Prefix string `xml:"Prefix"`
}

type listUploadsResult struct {
	XMLName            xml.Name       `xml:"ListMultipartUploadsResult"`
	Bucket             string         `xml:"Bucket"`
	KeyMarker          string         `xml:"KeyMarker"`
	UploadIDMarker     string         `xml:"UploadIdMarker"`
	NextKeyMarker      string         `xml:"NextKeyMarker,omitempty"`
	NextUploadIDMarker string         `xml:"NextUploadIdMarker,omitempty"`
	Prefix             string         `xml:"Prefix"`
	Delimiter          string         `xml:"Delimiter,omitempty"`
	MaxUploads         int            `xml:"MaxUploads"`
	IsTruncated        bool           `xml:"IsTruncated"`
	EncodingType       string         `xml:"EncodingType,omitempty"`
	Uploads            []listedUpload `xml:"Upload"`
	CommonPrefixes     []commonPrefix `xml:"CommonPrefixes"`
}
