package objectops

import (
	"net/url"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
)

const (
	metadataDirectiveCopy    = "COPY"
	metadataDirectiveReplace = "REPLACE"
	invalidCopySourceText    = "Copy Source must mention the source bucket and key: sourcebucket/sourcekey"
)

type copySource struct {
	bucket string
	key    string
}

func parseCopySource(header string) (copySource, error) {
	path, rawQuery, _ := strings.Cut(header, "?")
	if rawQuery != "" {
		query, err := url.ParseQuery(rawQuery)
		if err != nil {
			return copySource{}, invalidArgument(invalidCopySourceText)
		}
		if err := checkVersionID(query); err != nil {
			return copySource{}, err
		}
	}
	decoded, err := url.PathUnescape(strings.TrimPrefix(path, "/"))
	if err != nil {
		return copySource{}, invalidArgument(invalidCopySourceText)
	}
	name, key, found := strings.Cut(decoded, "/")
	if !found || key == "" || bucket.BucketName(name).Validate() != nil {
		return copySource{}, invalidArgument(invalidCopySourceText)
	}
	return copySource{bucket: name, key: key}, nil
}

func parseMetadataDirective(value string) (replace bool, err error) {
	switch value {
	case "", metadataDirectiveCopy:
		return false, nil
	case metadataDirectiveReplace:
		return true, nil
	}
	return false, invalidArgument("Unknown metadata directive.")
}

func checkTaggingDirective(value string) error {
	switch value {
	case "", metadataDirectiveCopy, metadataDirectiveReplace:
		return nil
	}
	return invalidArgument("Unknown tagging directive.")
}
