package service

import "errors"

// A protection reselect happens before an upstream send. Keep it request-local
// when endpoint-specific error handling would otherwise write an upstream 502.
func schedulingProtectionRequestLocalError(err error) *UpstreamFailoverError {
	var scoped *UpstreamFailoverError
	if errors.As(err, &scoped) && scoped.PluginAdmissionRejected && scoped.Scope == GatewayFailureScopeRequest &&
		(scoped.ProtectionOrdinaryFallback || scoped.Reason == "scheduling_protection_scope_changed") {
		return scoped
	}
	return nil
}
