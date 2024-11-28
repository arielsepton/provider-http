package datapatcher

import (
	"fmt"
	"regexp"
	"strings"

	"strconv"

	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane-contrib/provider-http/apis/common"
	httpClient "github.com/crossplane-contrib/provider-http/internal/clients/http"
	"github.com/crossplane-contrib/provider-http/internal/jq"
	json_util "github.com/crossplane-contrib/provider-http/internal/json"
	kubehandler "github.com/crossplane-contrib/provider-http/internal/kube-handler"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/pkg/errors"
)

const (
	errEmptyKey    = "Warning, value at field %s is empty, skipping opetation for: %s"
	errConvertData = "failed to convert data to map"
	errPatchFailed = "failed to patch secret, %s"
)

const (
	secretPattern = `\{\{\s*([^:{}\s]+):([^:{}\s]+):([^:{}\s]+)\s*\}\}`
)

var re = regexp.MustCompile(secretPattern)

// findPlaceholders finds all placeholders in the provided string.
func findPlaceholders(value string) []string {
	return re.FindAllString(value, -1)
}

// removeDuplicates removes duplicate strings from the given slice.
func removeDuplicates(strSlice []string) []string {
	unique := make(map[string]struct{})
	var result []string

	for _, str := range strSlice {
		if _, ok := unique[str]; !ok {
			result = append(result, str)
			unique[str] = struct{}{}
		}
	}

	return result
}

// parsePlaceholder parses a placeholder string and returns its components.
func parsePlaceholder(placeholder string) (name, namespace, key string, ok bool) {
	matches := re.FindStringSubmatch(placeholder)

	if len(matches) != 4 {
		return "", "", "", false
	}

	return matches[1], matches[2], matches[3], true
}

// replacePlaceholderWithSecretValue replaces a placeholder with the value from a secret.
func replacePlaceholderWithSecretValue(originalString, old string, secret *corev1.Secret, key string) string {
	replacementString := string(secret.Data[key])
	return strings.ReplaceAll(originalString, old, replacementString)
}

// patchSecretsToValue patches secrets referenced in the provided value.
func patchSecretsToValue(ctx context.Context, localKube client.Client, valueToHandle string, logger logging.Logger) (string, error) {
	placeholders := removeDuplicates(findPlaceholders(valueToHandle))
	for _, placeholder := range placeholders {

		name, namespace, key, ok := parsePlaceholder(placeholder)
		if !ok {
			return valueToHandle, nil
		}
		secret, err := kubehandler.GetSecret(ctx, localKube, name, namespace)
		if err != nil {
			logger.Info(fmt.Sprintf(errPatchFailed, err.Error()))
			return "", err
		}

		valueToHandle = replacePlaceholderWithSecretValue(valueToHandle, placeholder, secret, key)
	}

	return valueToHandle, nil

}

// patchValuesToSecret patches values to a secret.
func patchValuesToSecret(ctx context.Context, localKube client.Client, logger logging.Logger, data *httpClient.HttpResponse, secret *corev1.Secret, secretInjectionConfig common.SecretInjectionConfig) (bool, error) {
	// support for single key mapping
	if secretInjectionConfig.KeyMappings == nil {
		// TODO: check in debug if secret here is updated (should because of ref)
		_, err := patchValueToSecret(logger, data, secret, secretInjectionConfig.SecretKey, secretInjectionConfig.ResponsePath)
		if err != nil {
			return false, err
		}

		return true, nil
	}

	for _, keyMapping := range secretInjectionConfig.KeyMappings {
		var err error
		// TODO: check in debug if secret here is updated (should because of ref)
		_, err = patchValueToSecret(logger, data, secret, keyMapping.SecretKey, keyMapping.ResponsePath)
		if err != nil {
			return false, err
		}
	}

	return true, nil
}

// patchValueToSecret patches a value to a secret.
func patchValueToSecret(logger logging.Logger, data *httpClient.HttpResponse, secret *corev1.Secret, secretKey string, requestFieldPath string) (*corev1.Secret, error) {
	valueToPatch, err := parseAnyJQStringFromResponseData(logger, data, requestFieldPath)
	if err != nil {
		return nil, err
	}

	if secret.Data == nil {
		secret.Data = make(map[string][]byte)
	}

	secret.Data[secretKey] = []byte(valueToPatch)

	placeholder := fmt.Sprintf("{{%s:%s:%s}}", secret.Name, secret.Namespace, secretKey)
	if len(valueToPatch) != 0 {
		// patch the {{name:namespace:key}} of secret instead of the sensitive value
		data.Body = strings.ReplaceAll(data.Body, valueToPatch, placeholder)

		for _, headersList := range data.Headers {
			for i, header := range headersList {
				if len(valueToPatch) != 0 {
					newHeader := strings.ReplaceAll(header, valueToPatch, placeholder)
					headersList[i] = newHeader
				}
			}
		}
	}

	return secret, nil
}

// patchValueToMap patches a value to a map.
func patchValueToMap(logger logging.Logger, data *httpClient.HttpResponse, mapToPatch map[string]string) (map[string]string, error) {
	for key := range mapToPatch {
		valueToPatch, err := parseAnyJQStringFromResponseData(logger, data, mapToPatch[key])
		if err != nil {
			return nil, err
		}

		mapToPatch[key] = valueToPatch
	}

	return mapToPatch, nil
}

// patchSecretsInMap traverses a map and patches secrets into any string values.
func patchSecretsInMap(ctx context.Context, localKube client.Client, data map[string]interface{}, logger logging.Logger) error {
	for key, value := range data {
		switch v := value.(type) {
		case string:
			patchedValue, err := patchSecretsToValue(ctx, localKube, v, logger)
			if err != nil {
				return err
			}
			data[key] = patchedValue

		case map[string]interface{}:
			err := patchSecretsInMap(ctx, localKube, v, logger)
			if err != nil {
				return err
			}

		case []interface{}:
			err := patchSecretsInSlice(ctx, localKube, v, logger)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// patchSecretsInStringMap traverses a map of string to string and patches secrets into any string values.
func patchSecretsInStringMap(ctx context.Context, localKube client.Client, data map[string]string, logger logging.Logger) (map[string]string, error) {
	for key, value := range data {
		patchedValue, err := patchSecretsToValue(ctx, localKube, value, logger)
		if err != nil {
			return nil, err
		}
		data[key] = patchedValue
	}

	return data, nil
}

// parseAnyJQStringFromResponseData parses a jq query from the response data.
func parseAnyJQStringFromResponseData(logger logging.Logger, data *httpClient.HttpResponse, fieldPath string) (string, error) {
	dataMap, err := json_util.StructToMap(data)
	if err != nil {
		return "", errors.Wrap(err, errConvertData)
	}

	json_util.ConvertJSONStringsToMaps(&dataMap)

	valueToPatch, err := jq.ParseString(fieldPath, dataMap)
	if err != nil {
		boolResult, err := jq.ParseBool(fieldPath, dataMap)
		if err != nil {
			valueToPatch = ""
		} else {
			valueToPatch = strconv.FormatBool(boolResult)
		}
	}

	if valueToPatch == "" {
		logger.Info(fmt.Sprintf(errEmptyKey, fieldPath, fmt.Sprint(data)))
		return "", nil
	}

	return valueToPatch, nil
}
