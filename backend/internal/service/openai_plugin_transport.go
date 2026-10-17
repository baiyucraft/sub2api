package service

import "net/http"

func (s *OpenAIGatewayService) SetPluginManager(manager *PluginManager) {
	s.pluginManager = manager
}

// doOpenAIUpstream 只在 OpenAI OAuth 能力绑定已启用时把真实请求交给插件。
// 插件返回标准 http.Response，响应解析、错误映射、SSE 和计费仍由现有核心链处理。
func (s *OpenAIGatewayService) doOpenAIUpstream(request *http.Request, proxyURL string, account *Account) (response *http.Response, err error) {
	// This shared entry is the physical send boundary for both plugin and HTTP
	// transports. In-place upstream retries therefore receive a fresh TTFT
	// attempt clock while retaining the logical request/session identity.
	if request != nil {
		request = request.WithContext(prepareOpenAITTFTPhysicalAttempt(request.Context()))
	}
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			if response != nil {
				response.Request = request
			}
			return response, err
		}
	}
	if err := runSchedulingProtectionBeforeSend(request.Context(), account); err != nil {
		return nil, err
	}
	if err := MarkSchedulingProtectionTrialSent(request.Context(), account, openAITTFTGuardPreparedModel(request.Context())); err != nil {
		return nil, err
	}
	markOpenAITTFTGuardSend(request.Context(), account, "", "openai")
	response, err = s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
	if response != nil {
		response.Request = request
	}
	return response, err
}

// doOpenAIAccountTestUpstream 让 OpenAI OAuth 账号测试与真实转发使用同一插件路径。
// API Key 和未命中插件的账号保持各自原有的 HTTPUpstream 行为。
func (s *AccountTestService) doOpenAIAccountTestUpstream(
	request *http.Request,
	proxyURL string,
	account *Account,
	useTLSFallback bool,
) (*http.Response, error) {
	if s.pluginManager != nil {
		response, handled, err := s.pluginManager.RoundTripOpenAIOAuth(request.Context(), request, proxyURL, account)
		if handled {
			return response, err
		}
	}
	if useTLSFallback {
		return s.httpUpstream.DoWithTLS(
			request,
			proxyURL,
			account.ID,
			account.Concurrency,
			s.tlsFPProfileService.ResolveTLSProfile(account),
		)
	}
	return s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
}
