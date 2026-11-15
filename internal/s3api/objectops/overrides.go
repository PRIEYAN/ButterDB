package objectops

import (
	"net/url"
	"strconv"

	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
)

const anonymousOverrideText = "Request specific response headers cannot be used for anonymous GET requests."

var overrideParameters = []struct {
	query  string
	header string
}{
	{"response-content-type", "Content-Type"},
	{"response-content-language", "Content-Language"},
	{"response-expires", "Expires"},
	{"response-cache-control", "Cache-Control"},
	{"response-content-disposition", "Content-Disposition"},
	{"response-content-encoding", "Content-Encoding"},
}

func responseOverrides(query url.Values, signed bool) (map[string]string, error) {
	overrides := map[string]string{}
	for _, parameter := range overrideParameters {
		values, present := query[parameter.query]
		if !present {
			continue
		}
		if !signed {
			return nil, invalidRequest(anonymousOverrideText)
		}
		value := values[0]
		if len(value) > limits.MaxSystemHeaderBytes || !validMetaValue(value) {
			return nil, invalidArgument("Invalid value for " + parameter.query)
		}
		overrides[parameter.header] = value
	}
	return overrides, nil
}

func parsePartNumber(query url.Values, rangeHeader string) (bool, error) {
	values, present := query["partNumber"]
	if !present {
		return false, nil
	}
	if rangeHeader != "" {
		return false, invalidRequest("Cannot specify both Range header and partNumber query parameter")
	}
	number, err := strconv.Atoi(values[0])
	if err != nil || number != 1 {
		return false, errInvalidPartNumber()
	}
	return true, nil
}
