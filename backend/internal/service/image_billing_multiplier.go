package service

func resolveImageRateMultiplier(apiKey *APIKey, effectiveGroupMultiplier float64) float64 {
	if apiKey != nil && apiKey.Group != nil && apiKey.Group.ImageRateIndependent {
		if apiKey.Group.ImageRateMultiplier < 0 {
			return 0
		}
		return apiKey.Group.ImageRateMultiplier
	}
	return effectiveGroupMultiplier
}

// resolveImageRateMultiplierForUser preserves an explicit user/group zero
// override even when the group has an independent image multiplier. The
// independent image setting is a group-level fallback; it must not turn a
// deliberately free user request back into a charge.
func resolveImageRateMultiplierForUser(apiKey *APIKey, effectiveGroupMultiplier float64, explicitZero bool) float64 {
	if explicitZero {
		return 0
	}
	return resolveImageRateMultiplier(apiKey, effectiveGroupMultiplier)
}

func resolveVideoRateMultiplier(apiKey *APIKey, effectiveGroupMultiplier float64) float64 {
	if apiKey != nil && apiKey.Group != nil && apiKey.Group.VideoRateIndependent {
		if apiKey.Group.VideoRateMultiplier < 0 {
			return 0
		}
		return apiKey.Group.VideoRateMultiplier
	}
	return effectiveGroupMultiplier
}

func resolveVideoRateMultiplierForUser(apiKey *APIKey, effectiveGroupMultiplier float64, explicitZero bool) float64 {
	if explicitZero {
		return 0
	}
	return resolveVideoRateMultiplier(apiKey, effectiveGroupMultiplier)
}
