package s3err

const (
	CodeAccessControlListNotSupported                  Code = "AccessControlListNotSupported"
	CodeAccessDenied                                   Code = "AccessDenied"
	CodeAccessForbidden                                Code = "AccessForbidden"
	CodeAuthorizationHeaderMalformed                   Code = "AuthorizationHeaderMalformed"
	CodeAuthorizationQueryParametersError              Code = "AuthorizationQueryParametersError"
	CodeBadDigest                                      Code = "BadDigest"
	CodeBadRequest                                     Code = "BadRequest"
	CodeBucketAlreadyOwnedByYou                        Code = "BucketAlreadyOwnedByYou"
	CodeBucketNotEmpty                                 Code = "BucketNotEmpty"
	CodeEntityTooLarge                                 Code = "EntityTooLarge"
	CodeEntityTooSmall                                 Code = "EntityTooSmall"
	CodeIncompleteBody                                 Code = "IncompleteBody"
	CodeInternalError                                  Code = "InternalError"
	CodeInvalidAccessKeyId                             Code = "InvalidAccessKeyId"
	CodeInvalidArgument                                Code = "InvalidArgument"
	CodeInvalidBucketName                              Code = "InvalidBucketName"
	CodeInvalidDigest                                  Code = "InvalidDigest"
	CodeInvalidPart                                    Code = "InvalidPart"
	CodeInvalidPartNumber                              Code = "InvalidPartNumber"
	CodeInvalidPartOrder                               Code = "InvalidPartOrder"
	CodeInvalidRange                                   Code = "InvalidRange"
	CodeInvalidRequest                                 Code = "InvalidRequest"
	CodeInvalidStorageClass                            Code = "InvalidStorageClass"
	CodeInvalidToken                                   Code = "InvalidToken"
	CodeInvalidURI                                     Code = "InvalidURI"
	CodeKeyTooLongError                                Code = "KeyTooLongError"
	CodeMalformedXML                                   Code = "MalformedXML"
	CodeMaxMessageLengthExceeded                       Code = "MaxMessageLengthExceeded"
	CodeMetadataTooLarge                               Code = "MetadataTooLarge"
	CodeMethodNotAllowed                               Code = "MethodNotAllowed"
	CodeMissingContentLength                           Code = "MissingContentLength"
	CodeMissingRequestBodyError                        Code = "MissingRequestBodyError"
	CodeNoSuchBucket                                   Code = "NoSuchBucket"
	CodeNoSuchBucketPolicy                             Code = "NoSuchBucketPolicy"
	CodeNoSuchCORSConfiguration                        Code = "NoSuchCORSConfiguration"
	CodeNoSuchKey                                      Code = "NoSuchKey"
	CodeNoSuchLifecycleConfiguration                   Code = "NoSuchLifecycleConfiguration"
	CodeNoSuchPublicAccessBlockConfiguration           Code = "NoSuchPublicAccessBlockConfiguration"
	CodeNoSuchTagSet                                   Code = "NoSuchTagSet"
	CodeNoSuchUpload                                   Code = "NoSuchUpload"
	CodeNoSuchWebsiteConfiguration                     Code = "NoSuchWebsiteConfiguration"
	CodeNotImplemented                                 Code = "NotImplemented"
	CodeObjectLockConfigurationNotFoundError           Code = "ObjectLockConfigurationNotFoundError"
	CodeOperationAborted                               Code = "OperationAborted"
	CodePreconditionFailed                             Code = "PreconditionFailed"
	CodeRequestTimeTooSkewed                           Code = "RequestTimeTooSkewed"
	CodeRequestTimeout                                 Code = "RequestTimeout"
	CodeServerSideEncryptionConfigurationNotFoundError Code = "ServerSideEncryptionConfigurationNotFoundError"
	CodeServiceUnavailable                             Code = "ServiceUnavailable"
	CodeSignatureDoesNotMatch                          Code = "SignatureDoesNotMatch"
	CodeSlowDown                                       Code = "SlowDown"
	CodeXAmzContentSHA256Mismatch                      Code = "XAmzContentSHA256Mismatch"
)

type codeInfo struct {
	code    Code
	status  int
	message string
}

func catalogue() []codeInfo {
	return []codeInfo{
		{CodeAccessControlListNotSupported, 400, "The bucket does not allow ACLs"},
		{CodeAccessDenied, 403, "Access Denied"},
		{CodeAccessForbidden, 403, "CORSResponse: This CORS request is not allowed. This is usually because the evalution of Origin, request method / Access-Control-Request-Method or Access-Control-Request-Headers are not whitelisted by the resource's CORS spec."},
		{CodeAuthorizationHeaderMalformed, 400, "The authorization header is malformed."},
		{CodeAuthorizationQueryParametersError, 400, "Error parsing the X-Amz-Credential parameter."},
		{CodeBadDigest, 400, "The Content-MD5 or checksum value that you specified did not match what the server received."},
		{CodeBadRequest, 400, "Bad Request"},
		{CodeBucketAlreadyOwnedByYou, 409, "Your previous request to create the named bucket succeeded and you already own it."},
		{CodeBucketNotEmpty, 409, "The bucket you tried to delete is not empty"},
		{CodeEntityTooLarge, 400, "Your proposed upload exceeds the maximum allowed size"},
		{CodeEntityTooSmall, 400, "Your proposed upload is smaller than the minimum allowed size"},
		{CodeIncompleteBody, 400, "You did not provide the number of bytes specified by the Content-Length HTTP header"},
		{CodeInternalError, 500, "We encountered an internal error. Please try again."},
		{CodeInvalidAccessKeyId, 403, "The AWS Access Key Id you provided does not exist in our records."},
		{CodeInvalidArgument, 400, "Invalid Argument"},
		{CodeInvalidBucketName, 400, "The specified bucket is not valid."},
		{CodeInvalidDigest, 400, "The Content-MD5 you specified is not valid."},
		{CodeInvalidPart, 400, "One or more of the specified parts could not be found. The part may not have been uploaded, or the specified entity tag may not match the part's entity tag."},
		{CodeInvalidPartNumber, 416, "The requested partnumber is not satisfiable"},
		{CodeInvalidPartOrder, 400, "The list of parts was not in ascending order. The parts list must be specified in order by part number."},
		{CodeInvalidRange, 416, "The requested range is not satisfiable"},
		{CodeInvalidRequest, 400, "Invalid Request"},
		{CodeInvalidStorageClass, 400, "The storage class you specified is not valid"},
		{CodeInvalidToken, 400, "The provided token is malformed or otherwise invalid."},
		{CodeInvalidURI, 400, "Couldn't parse the specified URI."},
		{CodeKeyTooLongError, 400, "Your key is too long"},
		{CodeMalformedXML, 400, "The XML you provided was not well-formed or did not validate against our published schema."},
		{CodeMaxMessageLengthExceeded, 400, "Your request was too big."},
		{CodeMetadataTooLarge, 400, "Your metadata headers exceed the maximum allowed metadata size."},
		{CodeMethodNotAllowed, 405, "The specified method is not allowed against this resource."},
		{CodeMissingContentLength, 411, "You must provide the Content-Length HTTP header."},
		{CodeMissingRequestBodyError, 400, "Request Body is empty"},
		{CodeNoSuchBucket, 404, "The specified bucket does not exist"},
		{CodeNoSuchBucketPolicy, 404, "The bucket policy does not exist"},
		{CodeNoSuchCORSConfiguration, 404, "The CORS configuration does not exist"},
		{CodeNoSuchKey, 404, "The specified key does not exist."},
		{CodeNoSuchLifecycleConfiguration, 404, "The lifecycle configuration does not exist"},
		{CodeNoSuchPublicAccessBlockConfiguration, 404, "The public access block configuration was not found"},
		{CodeNoSuchTagSet, 404, "The TagSet does not exist"},
		{CodeNoSuchUpload, 404, "The specified upload does not exist. The upload ID may be invalid, or the upload may have been aborted or completed."},
		{CodeNoSuchWebsiteConfiguration, 404, "The specified bucket does not have a website configuration"},
		{CodeNotImplemented, 501, "A header or operation you provided implies functionality that is not implemented"},
		{CodeObjectLockConfigurationNotFoundError, 404, "Object Lock configuration does not exist for this bucket"},
		{CodeOperationAborted, 409, "A conflicting operation is currently in progress against this resource. Please try again."},
		{CodePreconditionFailed, 412, "At least one of the pre-conditions you specified did not hold"},
		{CodeRequestTimeTooSkewed, 403, "The difference between the request time and the current time is too large."},
		{CodeRequestTimeout, 400, "Your socket connection to the server was not read from or written to within the timeout period."},
		{CodeServerSideEncryptionConfigurationNotFoundError, 404, "The server side encryption configuration was not found"},
		{CodeServiceUnavailable, 503, "butterDB is starting or is not linked to Telegram. Please retry."},
		{CodeSignatureDoesNotMatch, 403, "The request signature we calculated does not match the signature you provided. Check your key and signing method."},
		{CodeSlowDown, 503, "Please reduce your request rate."},
		{CodeXAmzContentSHA256Mismatch, 400, "The provided 'x-amz-content-sha256' header does not match what was computed."},
	}
}

func Codes() []Code {
	entries := catalogue()
	codes := make([]Code, 0, len(entries))
	for _, entry := range entries {
		codes = append(codes, entry.code)
	}
	return codes
}

func lookup(code Code) (codeInfo, bool) {
	for _, entry := range catalogue() {
		if entry.code == code {
			return entry, true
		}
	}
	return codeInfo{}, false
}

func StatusOf(code Code) int {
	if info, ok := lookup(code); ok {
		return info.status
	}
	return 500
}

func New(code Code, message string) APIError {
	info, ok := lookup(code)
	if !ok {
		info, _ = lookup(CodeInternalError)
	}
	if message == "" {
		message = info.message
	}
	return APIError{Code: info.code, Status: info.status, Message: message}
}

func (e APIError) Error() string {
	return "s3err: " + string(e.Code) + ": " + e.Message
}
