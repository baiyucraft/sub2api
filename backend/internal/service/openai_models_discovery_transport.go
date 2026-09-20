package service

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

type openAIModelsDiscoveryContextKey struct{}

// This marker belongs to one internally constructed catalog request, not to
// its parent context or to arbitrary requests carrying the same headers.
type openAIModelsDiscoveryRequest struct {
	request   *http.Request
	url       string
	accountID int64
	identity  string
}

func markOpenAIModelsDiscoveryRequest(req *http.Request, account *Account) {
	if req == nil || req.URL == nil || account == nil {
		return
	}
	marker := openAIModelsDiscoveryRequest{
		request: req, url: req.URL.String(), accountID: account.ID,
		identity: PluginAccountIdentityRevision(account),
	}
	*req = *req.WithContext(context.WithValue(req.Context(), openAIModelsDiscoveryContextKey{}, marker))
}

func (marker openAIModelsDiscoveryRequest) matches(req *http.Request, account *Account) bool {
	if req == nil || req.URL == nil || account == nil || marker.request != req ||
		marker.accountID != account.ID || marker.identity != PluginAccountIdentityRevision(account) ||
		marker.url != req.URL.String() || req.Method != http.MethodGet ||
		(req.Body != nil && req.Body != http.NoBody) || req.ContentLength != 0 || len(req.TransferEncoding) != 0 {
		return false
	}
	if _, business := req.Context().Value(pluginRequestMetadataKey{}).(pluginRequestMetadata); business {
		return false
	}
	endpoint, err := url.Parse(chatgptCodexModelsURL)
	return err == nil && endpoint.Scheme != "" && endpoint.Host != "" &&
		req.URL.Scheme == endpoint.Scheme && req.URL.Host == endpoint.Host &&
		req.URL.EscapedPath() == endpoint.EscapedPath() && req.Host == req.URL.Host &&
		req.URL.User == nil && req.URL.Opaque == "" && req.URL.Fragment == ""
}

func (s *OpenAIGatewayService) roundTripOpenAIModelsDiscovery(req *http.Request, proxyURL string, account *Account) (*http.Response, bool, error) {
	if s == nil || s.pluginManager == nil {
		return nil, false, nil
	}
	if req == nil {
		return nil, true, errors.New("model discovery request is nil")
	}
	ctx := req.Context()
	if marker, ok := ctx.Value(openAIModelsDiscoveryContextKey{}).(openAIModelsDiscoveryRequest); ok {
		if !marker.matches(req, account) {
			return nil, true, &PluginAdmissionError{AccountID: marker.accountID}
		}
		if err := ctx.Err(); err != nil {
			return nil, true, err
		}
		route, err := s.pluginManager.currentScopedRoute(ctx)
		if route == nil && err != nil {
			return nil, true, &PluginAdmissionError{AccountID: account.ID}
		}
		// Only the confirmed scoped route is model admission. Legacy transport
		// plugins keep their original routing, including unavailable errors.
		if route != nil && route.scope != nil {
			return nil, false, nil
		}
	}
	return s.pluginManager.RoundTripOpenAIOAuth(ctx, req, proxyURL, account)
}
