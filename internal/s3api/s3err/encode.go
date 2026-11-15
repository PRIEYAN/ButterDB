package s3err

import (
	"bytes"
	"encoding/xml"
)

const xmlDeclaration = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"

func Encode(e APIError, requestID, hostID string) []byte {
	var buf bytes.Buffer
	buf.WriteString("<Error>")
	writeElement(&buf, "Code", string(e.Code))
	writeElement(&buf, "Message", e.Message)
	if e.Key != "" {
		writeElement(&buf, "Key", e.Key)
	}
	if e.BucketName != "" {
		writeElement(&buf, "BucketName", e.BucketName)
	}
	if resource := resourceOf(e); resource != "" {
		writeElement(&buf, "Resource", resource)
	}
	for _, field := range e.Extra {
		writeElement(&buf, field.Name, field.Value)
	}
	writeElement(&buf, "RequestId", requestID)
	writeElement(&buf, "HostId", hostID)
	buf.WriteString("</Error>")
	return buf.Bytes()
}

func resourceOf(e APIError) string {
	if e.Resource != "" {
		return e.Resource
	}
	if e.BucketName == "" {
		return ""
	}
	if e.Key == "" {
		return "/" + e.BucketName
	}
	return "/" + e.BucketName + "/" + e.Key
}

func writeElement(buf *bytes.Buffer, name, value string) {
	buf.WriteString("<" + name + ">")
	_ = xml.EscapeText(buf, []byte(value))
	buf.WriteString("</" + name + ">")
}
