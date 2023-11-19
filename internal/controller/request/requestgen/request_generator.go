package requestgen

import (
	"fmt"
	"strings"

	"github.com/arielsepton/provider-http/apis/request/v1alpha1"
	"github.com/arielsepton/provider-http/internal/controller/request/requestprocessing"
	json_util "github.com/arielsepton/provider-http/internal/json"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"golang.org/x/exp/maps"
)

type RequestDetails struct {
	Url     string
	Body    string
	Headers map[string][]string
}

// GenerateRequestDetails generates request details.
func GenerateRequestDetails(methodMapping v1alpha1.Mapping, forProvider v1alpha1.RequestParameters, response v1alpha1.Response, logger logging.Logger) (RequestDetails, error, bool) {
	jqObject := GenerateRequestObject(forProvider, response)
	url, err := generateURL(methodMapping.URL, jqObject, logger)
	if err != nil {
		return RequestDetails{}, err, false
	}

	body, err := generateBody(methodMapping.Body, jqObject, logger)
	if err != nil {
		return RequestDetails{}, err, false
	}

	headers, err := generateHeaders(coalesceHeaders(methodMapping.Headers, forProvider.Headers), jqObject, logger)
	if err != nil {
		return RequestDetails{}, err, false
	}

	return RequestDetails{Body: body, Url: url, Headers: headers}, nil, true
}

// GenerateRequestObject creates a JSON-compatible map from the specified Request's ForProvider and Response fields.
// It merges the two maps, converts JSON strings to nested maps, and returns the resulting map.
func GenerateRequestObject(forProvider v1alpha1.RequestParameters, response v1alpha1.Response) map[string]interface{} {
	baseMap, _ := json_util.StructToMap(forProvider)
	statusMap, _ := json_util.StructToMap(map[string]interface{}{
		"response": response,
	})

	maps.Copy(baseMap, statusMap)
	json_util.ConvertJSONStringsToMaps(&baseMap)

	return baseMap
}

func IsRequestValid(requestDetails RequestDetails) bool {
	return (!strings.Contains(fmt.Sprint(requestDetails), "null")) && (requestDetails.Url != "")
}

// coalesceHeaders returns the non-nil headers, or the default headers if both are nil.
func coalesceHeaders(mappingHeaders, defaultHeaders map[string][]string) map[string][]string {
	if mappingHeaders != nil {
		return mappingHeaders
	}
	return defaultHeaders
}

// generateURL applies a JQ filter to generate a URL.
func generateURL(urlJQFilter string, jqObject map[string]interface{}, logger logging.Logger) (string, error) {
	getURL, err := requestprocessing.ApplyJQOnStr(urlJQFilter, jqObject, logger)
	if err != nil {
		return "", err
	}

	return getURL, nil
}

// generateBody applies a mapping body to generate the request body.
func generateBody(mappingBody string, jqObject map[string]interface{}, logger logging.Logger) (string, error) {
	jqQuery := requestprocessing.ConvertStringToJQQuery(mappingBody)
	body, err := requestprocessing.ApplyJQOnStr(jqQuery, jqObject, logger)
	if err != nil {
		return "", err
	}

	return body, nil
}

// generateHeaders applies JQ queries to generate headers.
func generateHeaders(headers map[string][]string, jqObject map[string]interface{}, logger logging.Logger) (map[string][]string, error) {
	generatedHeaders, err := requestprocessing.ApplyJQOnMapStrings(headers, jqObject, logger)
	if err != nil {
		return nil, err
	}

	return generatedHeaders, nil
}
