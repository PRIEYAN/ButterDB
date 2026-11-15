package router

import "github.com/PRIEYAN/ButterDB/internal/domain/access"

func Actions() map[Operation]access.Action {
	return map[Operation]access.Action{
		OpHeadBucket:              access.ActionList,
		OpGetBucketLocation:       access.ActionList,
		OpListObjectsV2:           access.ActionList,
		OpListObjects:             access.ActionList,
		OpListMultipartUploads:    access.ActionList,
		OpListParts:               access.ActionList,
		OpGetBucketPolicy:         access.ActionList,
		OpGetBucketPolicyStatus:   access.ActionList,
		OpGetBucketCors:           access.ActionList,
		OpGetBucketAcl:            access.ActionList,
		OpGetBucketVersioning:     access.ActionList,
		OpGetBucketEncryption:     access.ActionList,
		OpGetBucketConfig:         access.ActionList,
		OpGetObject:               access.ActionRead,
		OpHeadObject:              access.ActionRead,
		OpGetObjectAcl:            access.ActionRead,
		OpGetObjectTagging:        access.ActionRead,
		OpPutObject:               access.ActionWrite,
		OpCopyObject:              access.ActionWrite,
		OpCreateMultipartUpload:   access.ActionWrite,
		OpUploadPart:              access.ActionWrite,
		OpUploadPartCopy:          access.ActionWrite,
		OpCompleteMultipartUpload: access.ActionWrite,
		OpAbortMultipartUpload:    access.ActionWrite,
		OpPutObjectAcl:            access.ActionWrite,
		OpPutBucketAcl:            access.ActionWrite,
		OpPutObjectTagging:        access.ActionWrite,
		OpDeleteObjectTagging:     access.ActionWrite,
		OpDeleteObject:            access.ActionDelete,
		OpDeleteObjects:           access.ActionDelete,
	}
}

func ActionFor(op Operation) (access.Action, bool) {
	action, ok := Actions()[op]
	return action, ok
}
