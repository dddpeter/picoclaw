package providers

import (
	"github.com/sipeed/picoclaw/pkg/auth"
)

var getCredential = auth.GetCredential

// Provider factory layout (two levels):
//
//	CreateProvider(cfg)          — config-level entry: resolves the configured
//	                                default model (agents.defaults.model) via
//	                                ResolveModelConfig against model_list,
//	                                injects the global workspace, then delegates
//	                                to CreateProviderFromConfig.
//	                                (pkg/providers/legacy_provider.go)
//	CreateProviderFromConfig(mc) — single *config.ModelConfig → typed provider
//	                                instance; the only place mapping
//	                                protocol/provider keys to concrete provider
//	                                implementations.
//	                                (pkg/providers/factory_provider.go)
//
// Callers that already hold a resolved *config.ModelConfig should call
// CreateProviderFromConfig directly; CreateProvider is for bootstrapping
// from a whole config (gateway startup, CLI --model handling).
