package registry

import "net/url"

const (
	// maskedValue replaces a secret value entirely.
	maskedValue = "****"
	// maskedURLSuffix follows the host in a masked URL.
	maskedURLSuffix = "/****"
)

// maskKeepsHost lists the secret keys whose value is a URL: only the host of
// these is shown, because the path and query carry the credential.
var maskKeepsHost = map[string]bool{
	"webhook_url": true,
	"url":         true,
}

// MaskSecrets returns a copy of secrets that is safe to return to a client.
// URL secrets (webhook_url, url) keep only "https://<host>/****"; every other
// value becomes "****". A URL that does not parse to a host is masked fully.
// A nil or empty input yields an empty non-nil map, and the input is never
// modified.
func MaskSecrets(secrets map[string]string) map[string]string {
	masked := make(map[string]string, len(secrets))
	for key, value := range secrets {
		if maskKeepsHost[key] {
			masked[key] = maskURL(value)
			continue
		}
		masked[key] = maskedValue
	}
	return masked
}

// maskURL keeps only the host of rawURL, dropping userinfo, path, query and
// fragment. It fails closed to a full mask when rawURL has no usable host.
func maskURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return maskedValue
	}
	return "https://" + parsed.Host + maskedURLSuffix
}
