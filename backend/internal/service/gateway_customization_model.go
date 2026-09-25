package service

import (
	"context"
	"strings"
)

type channelCustomizationModelContextKey struct{}
type channelCustomizationBillingContextKey struct{}

type ChannelCustomizationModel struct {
	Original string
	Target   string
}

// ChannelCustomizationBillingSnapshot preserves the pre-customization billing
// identity while the request-scoped API key may point at a routing group.
type ChannelCustomizationBillingSnapshot struct {
	APIKey                  *APIKey
	Subscription            *UserSubscription
	SubscriptionWasResolved bool
}

func WithChannelCustomizationBillingSnapshot(ctx context.Context, apiKey *APIKey, subscription *UserSubscription, resolved bool) context.Context {
	if ctx == nil || apiKey == nil {
		return ctx
	}
	clone := *apiKey
	return context.WithValue(ctx, channelCustomizationBillingContextKey{}, ChannelCustomizationBillingSnapshot{
		APIKey:                  &clone,
		Subscription:            subscription,
		SubscriptionWasResolved: resolved,
	})
}

func ChannelCustomizationBillingSnapshotFromContext(ctx context.Context) (ChannelCustomizationBillingSnapshot, bool) {
	if ctx == nil {
		return ChannelCustomizationBillingSnapshot{}, false
	}
	snapshot, ok := ctx.Value(channelCustomizationBillingContextKey{}).(ChannelCustomizationBillingSnapshot)
	return snapshot, ok && snapshot.APIKey != nil
}

func ChannelCustomizationBillingAPIKey(ctx context.Context, fallback *APIKey) *APIKey {
	if snapshot, ok := ChannelCustomizationBillingSnapshotFromContext(ctx); ok {
		return snapshot.APIKey
	}
	return fallback
}

func ChannelCustomizationBillingSubscription(ctx context.Context, fallback *UserSubscription) *UserSubscription {
	if snapshot, ok := ChannelCustomizationBillingSnapshotFromContext(ctx); ok && snapshot.SubscriptionWasResolved {
		return snapshot.Subscription
	}
	return fallback
}

// WithChannelCustomizationModel preserves the client model independently of
// composite routing, which may replace RequestedPublicModel with its own alias.
func WithChannelCustomizationModel(ctx context.Context, original, target string) context.Context {
	if ctx == nil || strings.TrimSpace(original) == "" || strings.TrimSpace(target) == "" {
		return ctx
	}
	return context.WithValue(ctx, channelCustomizationModelContextKey{}, ChannelCustomizationModel{Original: original, Target: target})
}

func ChannelCustomizationModelFromContext(ctx context.Context) (ChannelCustomizationModel, bool) {
	if ctx == nil {
		return ChannelCustomizationModel{}, false
	}
	model, ok := ctx.Value(channelCustomizationModelContextKey{}).(ChannelCustomizationModel)
	return model, ok && model.Original != "" && model.Target != ""
}

// CustomizedChannelUsageFields keeps upstream/channel attribution while forcing
// customer billing to the public model. No pricing fallback to the target is
// permitted for customized requests.
func CustomizedChannelUsageFields(ctx context.Context, fields ChannelUsageFields, upstream string) ChannelUsageFields {
	model, ok := ChannelCustomizationModelFromContext(ctx)
	if !ok {
		return fields
	}
	chain := model.Original + "→" + model.Target
	if fields.ChannelMappedModel != "" && fields.ChannelMappedModel != model.Target {
		chain += "→" + fields.ChannelMappedModel
	}
	if upstream != "" && upstream != fields.ChannelMappedModel && upstream != model.Target {
		chain += "→" + upstream
	}
	fields.OriginalModel = model.Original
	fields.BillingModelSource = BillingModelSourceRequested
	fields.ModelMappingChain = chain
	return fields
}
