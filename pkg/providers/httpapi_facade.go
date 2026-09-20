// Compatibility facade: re-exports pkg/providers/httpapi types and
// constructors under the historic root-package import path. The
// implementations live in pkg/providers/httpapi — new code should import that
// package directly; this facade exists so external configs / plugins with the
// old import path keep compiling and is not extended with new symbols.
package providers

import httpapi "github.com/sipeed/picoclaw/pkg/providers/httpapi"

type (
	GeminiProvider = httpapi.GeminiProvider
	HTTPProvider   = httpapi.HTTPProvider
)

func NewGeminiProvider(
	apiKey string,
	apiBase string,
	proxy string,
	userAgent string,
	requestTimeoutSeconds int,
	extraBody map[string]any,
	customHeaders map[string]string,
) *GeminiProvider {
	return httpapi.NewGeminiProvider(apiKey, apiBase, proxy, userAgent, requestTimeoutSeconds, extraBody, customHeaders)
}

func NewHTTPProvider(apiKey, apiBase, proxy string) *HTTPProvider {
	return httpapi.NewHTTPProvider(apiKey, apiBase, proxy)
}

func NewHTTPProviderWithMaxTokensField(apiKey, apiBase, proxy, maxTokensField string) *HTTPProvider {
	return httpapi.NewHTTPProviderWithMaxTokensField(apiKey, apiBase, proxy, maxTokensField)
}

func NewHTTPProviderWithMaxTokensFieldAndRequestTimeout(
	apiKey, apiBase, proxy, maxTokensField, userAgent string,
	requestTimeoutSeconds int,
	extraBody map[string]any,
	customHeaders map[string]string,
) *HTTPProvider {
	return httpapi.NewHTTPProviderWithMaxTokensFieldAndRequestTimeout(
		apiKey,
		apiBase,
		proxy,
		maxTokensField,
		userAgent,
		requestTimeoutSeconds,
		extraBody,
		customHeaders,
	)
}
