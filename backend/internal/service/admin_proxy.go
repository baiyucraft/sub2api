package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/util/httputil"
)

// Proxy management implementations
func (s *adminServiceImpl) ListProxies(ctx context.Context, page, pageSize int, protocol, status, search string, sortBy, sortOrder string) ([]Proxy, int64, error) {
	if s.proxyIPGroupRepo == nil {
		params := pagination.PaginationParams{Page: page, PageSize: pageSize, SortBy: sortBy, SortOrder: sortOrder}
		proxies, result, err := s.proxyRepo.ListWithFilters(ctx, params, protocol, status, search)
		if err != nil {
			return nil, 0, err
		}
		return proxies, result.Total, nil
	}

	proxies, err := s.listNativeProxyBindings(ctx, protocol, status, search, sortBy, sortOrder, false)
	if err != nil {
		return nil, 0, err
	}
	total := int64(len(proxies))
	proxies = paginateProxySlice(proxies, page, pageSize)
	return proxies, total, nil
}

func (s *adminServiceImpl) ListProxiesWithAccountCount(ctx context.Context, page, pageSize int, protocol, status, search string, sortBy, sortOrder string) ([]ProxyWithAccountCount, int64, error) {
	if s.proxyIPGroupRepo == nil {
		return s.ListRealProxiesWithAccountCount(ctx, page, pageSize, protocol, status, search, sortBy, sortOrder)
	}

	proxies, err := s.listNativeProxyBindingsWithAccountCount(ctx, protocol, status, search, sortBy, sortOrder, true, false)
	if err != nil {
		return nil, 0, err
	}
	total := int64(len(proxies))
	proxies = paginateProxySlice(proxies, page, pageSize)
	return proxies, total, nil
}

func (s *adminServiceImpl) ListRealProxiesWithAccountCount(ctx context.Context, page, pageSize int, protocol, status, search string, sortBy, sortOrder string) ([]ProxyWithAccountCount, int64, error) {
	params := pagination.PaginationParams{Page: page, PageSize: pageSize, SortBy: sortBy, SortOrder: sortOrder}
	proxies, result, err := s.proxyRepo.ListWithFiltersAndAccountCount(ctx, params, protocol, status, search)
	if err != nil {
		return nil, 0, err
	}
	for i := range proxies {
		proxies[i].BindingType = proxyBindingTypeProxy
		id := proxies[i].ID
		proxies[i].ProxyID = &id
	}
	s.attachProxyLatency(ctx, proxies)
	return proxies, result.Total, nil
}

func (s *adminServiceImpl) GetAllProxies(ctx context.Context) ([]Proxy, error) {
	if s.proxyIPGroupRepo == nil {
		return s.proxyRepo.ListActive(ctx)
	}
	return s.listNativeProxyBindings(ctx, "", "", "", "id", "desc", true)
}

func (s *adminServiceImpl) GetAllProxiesWithAccountCount(ctx context.Context) ([]ProxyWithAccountCount, error) {
	if s.proxyIPGroupRepo != nil {
		return s.listNativeProxyBindingsWithAccountCount(ctx, "", "", "", "id", "desc", true, true)
	}
	proxies, err := s.proxyRepo.ListActiveWithAccountCount(ctx)
	if err != nil {
		return nil, err
	}
	s.attachProxyLatency(ctx, proxies)
	return proxies, nil
}

const (
	proxyBindingTypeProxy        = "proxy"
	proxyBindingTypeProxyIPGroup = "proxy_ip_group"
	proxyGroupProtocol           = "proxy_ip_group"
	proxyGroupStatusAvailable    = "available"
	proxyGroupStatusUnavailable  = "unavailable"
)

func validateAdminRealProxyID(id int64) error {
	if id < 0 {
		return infraerrors.BadRequest("PROXY_IP_GROUP_VIRTUAL_OPERATION_UNSUPPORTED", "virtual proxy-group IDs are only valid for account binding")
	}
	return nil
}

func paginateProxySlice[T any](items []T, page, pageSize int) []T {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = len(items)
	}
	start := (page - 1) * pageSize
	if start >= len(items) {
		return []T{}
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

func (s *adminServiceImpl) listAllFilteredProxies(ctx context.Context, protocol, status, search, sortBy, sortOrder string, withCount bool) ([]ProxyWithAccountCount, error) {
	const batchSize = 500
	var out []ProxyWithAccountCount
	for page := 1; ; page++ {
		params := pagination.PaginationParams{Page: page, PageSize: batchSize, SortBy: sortBy, SortOrder: sortOrder}
		var items []ProxyWithAccountCount
		var total int64
		var err error
		if withCount {
			var result *pagination.PaginationResult
			items, result, err = s.proxyRepo.ListWithFiltersAndAccountCount(ctx, params, protocol, status, search)
			if result != nil {
				total = result.Total
			}
		} else {
			var plain []Proxy
			var result *pagination.PaginationResult
			plain, result, err = s.proxyRepo.ListWithFilters(ctx, params, protocol, status, search)
			for i := range plain {
				items = append(items, ProxyWithAccountCount{Proxy: plain[i]})
			}
			if result != nil {
				total = result.Total
			}
		}
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if len(items) == 0 || int64(len(out)) >= total {
			break
		}
	}
	return out, nil
}

func (s *adminServiceImpl) listNativeProxyBindings(ctx context.Context, protocol, status, search, sortBy, sortOrder string, activeOnly bool) ([]Proxy, error) {
	items, err := s.listNativeProxyBindingsWithAccountCount(ctx, protocol, status, search, sortBy, sortOrder, false, activeOnly)
	if err != nil {
		return nil, err
	}
	out := make([]Proxy, 0, len(items))
	for i := range items {
		out = append(out, items[i].Proxy)
	}
	return out, nil
}

func (s *adminServiceImpl) listNativeProxyBindingsWithAccountCount(ctx context.Context, protocol, status, search, sortBy, sortOrder string, withCount, activeOnly bool) ([]ProxyWithAccountCount, error) {
	realStatus := status
	if activeOnly {
		realStatus = StatusActive
	}
	realItems, err := s.listAllFilteredProxies(ctx, protocol, realStatus, search, sortBy, sortOrder, withCount)
	if err != nil {
		return nil, err
	}
	groups, err := s.proxyIPGroupRepo.List(ctx)
	if err != nil {
		return nil, err
	}

	memberIDs := make([]int64, 0)
	for _, group := range groups {
		memberIDs = append(memberIDs, group.ProxyIDs...)
	}
	memberProxies, err := s.proxyRepo.ListByIDs(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	proxyByID := make(map[int64]Proxy, len(memberProxies))
	for i := range memberProxies {
		proxyByID[memberProxies[i].ID] = memberProxies[i]
	}
	now := time.Now()
	for _, group := range groups {
		if !matchesProxyBindingFilter(group.Name, proxyGroupProtocol, groupBindingStatus(group, proxyByID, now), protocol, status, search) {
			continue
		}
		available := 0
		for _, proxyID := range group.ProxyIDs {
			member, ok := proxyByID[proxyID]
			if ok && member.IsActive() && !member.IsExpired(now) {
				available++
			}
		}
		groupID := group.ID
		virtualID := -group.ID
		item := ProxyWithAccountCount{Proxy: Proxy{
			ID: virtualID, Name: group.Name, Protocol: proxyGroupProtocol,
			Status: groupBindingStatus(group, proxyByID, now), BindingType: proxyBindingTypeProxyIPGroup,
			ProxyIPGroupID: &groupID, MemberCount: len(group.ProxyIDs),
			AvailableMemberCount: available, PerIPConcurrency: group.PerIPConcurrency,
			CreatedAt: group.CreatedAt, UpdatedAt: group.UpdatedAt,
		}}
		if withCount {
			item.AccountCount, err = s.proxyIPGroupRepo.CountAccounts(ctx, group.ID)
			if err != nil {
				return nil, err
			}
		}
		realItems = append(realItems, item)
	}
	for i := range realItems {
		if realItems[i].BindingType == "" {
			realItems[i].BindingType = proxyBindingTypeProxy
			id := realItems[i].ID
			realItems[i].ProxyID = &id
		}
	}
	sortProxyBindings(realItems, sortBy, sortOrder)
	if withCount {
		s.attachProxyLatency(ctx, realItems)
	}
	return realItems, nil
}

func groupBindingStatus(group ProxyIPGroup, members map[int64]Proxy, now time.Time) string {
	for _, proxyID := range group.ProxyIDs {
		if proxy, ok := members[proxyID]; ok && proxy.IsActive() && !proxy.IsExpired(now) {
			return proxyGroupStatusAvailable
		}
	}
	return proxyGroupStatusUnavailable
}

func matchesProxyBindingFilter(name, bindingProtocol, bindingStatus, protocol, status, search string) bool {
	if protocol != "" && protocol != bindingProtocol {
		return false
	}
	if status != "" && status != bindingStatus {
		return false
	}
	search = strings.ToLower(strings.TrimSpace(search))
	return search == "" || strings.Contains(strings.ToLower(name), search)
}

func sortProxyBindings(items []ProxyWithAccountCount, sortBy, sortOrder string) {
	desc := strings.EqualFold(sortOrder, "desc")
	sort.SliceStable(items, func(i, j int) bool {
		cmp := 0
		switch strings.ToLower(sortBy) {
		case "name":
			cmp = strings.Compare(strings.ToLower(items[i].Name), strings.ToLower(items[j].Name))
		case "account_count":
			if items[i].AccountCount < items[j].AccountCount {
				cmp = -1
			} else if items[i].AccountCount > items[j].AccountCount {
				cmp = 1
			}
		case "created_at":
			if items[i].CreatedAt.Before(items[j].CreatedAt) {
				cmp = -1
			} else if items[i].CreatedAt.After(items[j].CreatedAt) {
				cmp = 1
			}
		default:
			if items[i].ID < items[j].ID {
				cmp = -1
			} else if items[i].ID > items[j].ID {
				cmp = 1
			}
		}
		if cmp == 0 {
			return items[i].ID < items[j].ID
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})
}

func (s *adminServiceImpl) GetProxy(ctx context.Context, id int64) (*Proxy, error) {
	if err := validateAdminRealProxyID(id); err != nil {
		return nil, err
	}
	return s.proxyRepo.GetByID(ctx, id)
}

func (s *adminServiceImpl) GetProxiesByIDs(ctx context.Context, ids []int64) ([]Proxy, error) {
	for _, id := range ids {
		if err := validateAdminRealProxyID(id); err != nil {
			return nil, err
		}
	}
	return s.proxyRepo.ListByIDs(ctx, ids)
}

func (s *adminServiceImpl) CreateProxy(ctx context.Context, input *CreateProxyInput) (*Proxy, error) {
	if !isJSONTimeInRange(input.ExpiresAt) {
		return nil, infraerrors.BadRequest("PROXY_EXPIRY_INVALID", "proxy expiry year must be between 0 and 9999")
	}
	// 规范化 fallback_mode
	mode := input.FallbackMode
	if mode == "" {
		mode = FallbackModeNone
	}
	// 校验：mode=proxy 必须有 backup
	if mode == FallbackModeProxy && input.BackupProxyID == nil {
		return nil, infraerrors.BadRequest("PROXY_BACKUP_REQUIRED", "backup proxy required when fallback_mode=proxy")
	}
	if input.ExpiryWarnDays < 0 {
		return nil, infraerrors.BadRequest("PROXY_WARN_DAYS_INVALID", "expiry_warn_days must be >= 0")
	}

	proxy := &Proxy{
		Name:           input.Name,
		Protocol:       input.Protocol,
		Host:           input.Host,
		Port:           input.Port,
		Username:       input.Username,
		Password:       input.Password,
		Status:         StatusActive,
		ExpiresAt:      input.ExpiresAt,
		FallbackMode:   mode,
		BackupProxyID:  input.BackupProxyID,
		ExpiryWarnDays: input.ExpiryWarnDays,
	}
	if err := s.proxyRepo.Create(ctx, proxy); err != nil {
		return nil, err
	}
	// Probe latency asynchronously so creation isn't blocked by network timeout.
	go s.probeProxyLatency(context.Background(), proxy)
	return proxy, nil
}

func (s *adminServiceImpl) UpdateProxy(ctx context.Context, id int64, input *UpdateProxyInput) (*Proxy, error) {
	if err := validateAdminRealProxyID(id); err != nil {
		return nil, err
	}
	if !isJSONTimeInRange(input.ExpiresAt) {
		return nil, infraerrors.BadRequest("PROXY_EXPIRY_INVALID", "proxy expiry year must be between 0 and 9999")
	}
	// 校验：backup_proxy_id 不能是自身
	if input.BackupProxyID != nil && *input.BackupProxyID == id {
		return nil, infraerrors.BadRequest("PROXY_BACKUP_SELF", "backup proxy cannot be itself")
	}
	proxy, err := s.proxyRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// Merge only supplied fields, then validate the resulting fallback configuration.
	mode := proxy.FallbackMode
	if input.FallbackMode != "" {
		mode = input.FallbackMode
	}
	backupID := proxy.BackupProxyID
	if input.BackupProxyID != nil || input.ClearBackupID {
		backupID = input.BackupProxyID
	}
	if mode == FallbackModeProxy && backupID == nil {
		return nil, infraerrors.BadRequest("PROXY_BACKUP_REQUIRED", "backup proxy required when fallback_mode=proxy")
	}
	if input.ExpiryWarnDays != nil && *input.ExpiryWarnDays < 0 {
		return nil, infraerrors.BadRequest("PROXY_WARN_DAYS_INVALID", "expiry_warn_days must be >= 0")
	}

	if input.Name != "" {
		proxy.Name = input.Name
	}
	if input.Protocol != "" {
		proxy.Protocol = input.Protocol
	}
	if input.Host != "" {
		proxy.Host = input.Host
	}
	if input.Port != 0 {
		proxy.Port = input.Port
	}
	if input.Username != nil {
		proxy.Username = *input.Username
	}
	if input.Password != nil {
		proxy.Password = *input.Password
	}
	if input.Status != "" {
		proxy.Status = input.Status
	}
	if input.ExpiresAt != nil || input.ClearExpiresAt {
		proxy.ExpiresAt = input.ExpiresAt
	}
	proxy.FallbackMode = mode
	proxy.BackupProxyID = backupID
	if input.ExpiryWarnDays != nil {
		proxy.ExpiryWarnDays = *input.ExpiryWarnDays
	}

	if err := s.proxyRepo.Update(ctx, proxy); err != nil {
		return nil, err
	}
	return proxy, nil
}

func (s *adminServiceImpl) DeleteProxy(ctx context.Context, id int64) error {
	if err := validateAdminRealProxyID(id); err != nil {
		return err
	}
	count, err := s.proxyRepo.CountAccountsByProxyID(ctx, id)
	if err != nil {
		return err
	}
	if count > 0 {
		return ErrProxyInUse
	}
	return s.proxyRepo.Delete(ctx, id)
}

func (s *adminServiceImpl) BatchDeleteProxies(ctx context.Context, ids []int64) (*ProxyBatchDeleteResult, error) {
	result := &ProxyBatchDeleteResult{}
	if len(ids) == 0 {
		return result, nil
	}

	for _, id := range ids {
		if err := validateAdminRealProxyID(id); err != nil {
			result.Skipped = append(result.Skipped, ProxyBatchDeleteSkipped{
				ID:     id,
				Reason: infraerrors.Message(err),
			})
			continue
		}
		count, err := s.proxyRepo.CountAccountsByProxyID(ctx, id)
		if err != nil {
			result.Skipped = append(result.Skipped, ProxyBatchDeleteSkipped{
				ID:     id,
				Reason: err.Error(),
			})
			continue
		}
		if count > 0 {
			result.Skipped = append(result.Skipped, ProxyBatchDeleteSkipped{
				ID:     id,
				Reason: ErrProxyInUse.Error(),
			})
			continue
		}
		if err := s.proxyRepo.Delete(ctx, id); err != nil {
			result.Skipped = append(result.Skipped, ProxyBatchDeleteSkipped{
				ID:     id,
				Reason: err.Error(),
			})
			continue
		}
		result.DeletedIDs = append(result.DeletedIDs, id)
	}

	return result, nil
}

func (s *adminServiceImpl) GetProxyAccounts(ctx context.Context, proxyID int64) ([]ProxyAccountSummary, error) {
	if err := validateAdminRealProxyID(proxyID); err != nil {
		return nil, err
	}
	return s.proxyRepo.ListAccountSummariesByProxyID(ctx, proxyID)
}

func (s *adminServiceImpl) CheckProxyExists(ctx context.Context, host string, port int, username, password string) (bool, error) {
	return s.proxyRepo.ExistsByHostPortAuth(ctx, host, port, username, password)
}

func (s *adminServiceImpl) TestProxy(ctx context.Context, id int64) (*ProxyTestResult, error) {
	if err := validateAdminRealProxyID(id); err != nil {
		return nil, err
	}
	proxy, err := s.proxyRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	proxyURL := proxy.URL()
	exitInfo, latencyMs, err := s.proxyProber.ProbeProxy(ctx, proxyURL)
	if err != nil {
		s.saveProxyLatency(ctx, id, &ProxyLatencyInfo{
			Success:   false,
			Message:   err.Error(),
			UpdatedAt: time.Now(),
		})
		return &ProxyTestResult{
			Success: false,
			Message: err.Error(),
		}, nil
	}

	latency := latencyMs
	s.saveProxyLatency(ctx, id, &ProxyLatencyInfo{
		Success:     true,
		LatencyMs:   &latency,
		Message:     "Proxy is accessible",
		IPAddress:   exitInfo.IP,
		Country:     exitInfo.Country,
		CountryCode: exitInfo.CountryCode,
		Region:      exitInfo.Region,
		City:        exitInfo.City,
		UpdatedAt:   time.Now(),
	})
	return &ProxyTestResult{
		Success:     true,
		Message:     "Proxy is accessible",
		LatencyMs:   latencyMs,
		IPAddress:   exitInfo.IP,
		City:        exitInfo.City,
		Region:      exitInfo.Region,
		Country:     exitInfo.Country,
		CountryCode: exitInfo.CountryCode,
	}, nil
}

func (s *adminServiceImpl) CheckProxyQuality(ctx context.Context, id int64) (*ProxyQualityCheckResult, error) {
	if err := validateAdminRealProxyID(id); err != nil {
		return nil, err
	}
	proxy, err := s.proxyRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	result := &ProxyQualityCheckResult{
		ProxyID:   id,
		Score:     100,
		Grade:     "A",
		CheckedAt: time.Now().Unix(),
		Items:     make([]ProxyQualityCheckItem, 0, len(proxyQualityTargets)+1),
	}

	proxyURL := proxy.URL()
	if s.proxyProber == nil {
		result.Items = append(result.Items, ProxyQualityCheckItem{
			Target:  "base_connectivity",
			Status:  "fail",
			Message: "代理探测服务未配置",
		})
		result.FailedCount++
		finalizeProxyQualityResult(result)
		s.saveProxyQualitySnapshot(ctx, id, result, nil)
		return result, nil
	}

	exitInfo, latencyMs, err := s.proxyProber.ProbeProxy(ctx, proxyURL)
	if err != nil {
		result.Items = append(result.Items, ProxyQualityCheckItem{
			Target:    "base_connectivity",
			Status:    "fail",
			LatencyMs: latencyMs,
			Message:   err.Error(),
		})
		result.FailedCount++
		finalizeProxyQualityResult(result)
		s.saveProxyQualitySnapshot(ctx, id, result, nil)
		return result, nil
	}

	result.ExitIP = exitInfo.IP
	result.Country = exitInfo.Country
	result.CountryCode = exitInfo.CountryCode
	result.BaseLatencyMs = latencyMs
	result.Items = append(result.Items, ProxyQualityCheckItem{
		Target:    "base_connectivity",
		Status:    "pass",
		LatencyMs: latencyMs,
		Message:   "代理出口连通正常",
	})
	result.PassedCount++

	client, err := httpclient.GetClient(httpclient.Options{
		ProxyURL:              proxyURL,
		Timeout:               proxyQualityRequestTimeout,
		ResponseHeaderTimeout: proxyQualityResponseHeaderTimeout,
	})
	if err != nil {
		result.Items = append(result.Items, ProxyQualityCheckItem{
			Target:  "http_client",
			Status:  "fail",
			Message: fmt.Sprintf("创建检测客户端失败: %v", err),
		})
		result.FailedCount++
		finalizeProxyQualityResult(result)
		s.saveProxyQualitySnapshot(ctx, id, result, exitInfo)
		return result, nil
	}

	for _, target := range proxyQualityTargets {
		item := runProxyQualityTarget(ctx, client, target)
		result.Items = append(result.Items, item)
		switch item.Status {
		case "pass":
			result.PassedCount++
		case "warn":
			result.WarnCount++
		case "challenge":
			result.ChallengeCount++
		default:
			result.FailedCount++
		}
	}

	finalizeProxyQualityResult(result)
	s.saveProxyQualitySnapshot(ctx, id, result, exitInfo)
	return result, nil
}

func runProxyQualityTarget(ctx context.Context, client *http.Client, target proxyQualityTarget) ProxyQualityCheckItem {
	item := ProxyQualityCheckItem{
		Target: target.Target,
	}

	req, err := http.NewRequestWithContext(ctx, target.Method, target.URL, nil)
	if err != nil {
		item.Status = "fail"
		item.Message = fmt.Sprintf("构建请求失败: %v", err)
		return item
	}
	req.Header.Set("Accept", "application/json,text/html,*/*")
	req.Header.Set("User-Agent", proxyQualityClientUserAgent)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		item.Status = "fail"
		item.LatencyMs = time.Since(start).Milliseconds()
		item.Message = fmt.Sprintf("请求失败: %v", err)
		return item
	}
	defer func() { _ = resp.Body.Close() }()
	item.LatencyMs = time.Since(start).Milliseconds()
	item.HTTPStatus = resp.StatusCode

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, proxyQualityMaxBodyBytes+1))
	if readErr != nil {
		item.Status = "fail"
		item.Message = fmt.Sprintf("读取响应失败: %v", readErr)
		return item
	}
	if int64(len(body)) > proxyQualityMaxBodyBytes {
		body = body[:proxyQualityMaxBodyBytes]
	}

	// Cloudflare challenge 检测
	if httputil.IsCloudflareChallengeResponse(resp.StatusCode, resp.Header, body) {
		item.Status = "challenge"
		item.CFRay = httputil.ExtractCloudflareRayID(resp.Header, body)
		item.Message = "命中 Cloudflare challenge"
		return item
	}

	if _, ok := target.AllowedStatuses[resp.StatusCode]; ok {
		// 白名单内的状态码均代表目标可达：2xx 表示接口直接可用，
		// 401/405 等是无鉴权探测的预期结果，同样视为连通正常，不再扣分。
		item.Status = "pass"
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			item.Message = fmt.Sprintf("HTTP %d", resp.StatusCode)
		} else {
			item.Message = fmt.Sprintf("HTTP %d（目标可达）", resp.StatusCode)
		}
		return item
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		item.Status = "warn"
		item.Message = "目标返回 429，可能存在频控"
		return item
	}

	item.Status = "fail"
	item.Message = fmt.Sprintf("非预期状态码: %d", resp.StatusCode)
	return item
}

func finalizeProxyQualityResult(result *ProxyQualityCheckResult) {
	if result == nil {
		return
	}
	score := 100 - result.WarnCount*10 - result.FailedCount*22 - result.ChallengeCount*30
	if score < 0 {
		score = 0
	}
	result.Score = score
	result.Grade = proxyQualityGrade(score)
	result.Summary = fmt.Sprintf(
		"通过 %d 项，告警 %d 项，失败 %d 项，挑战 %d 项",
		result.PassedCount,
		result.WarnCount,
		result.FailedCount,
		result.ChallengeCount,
	)
}

func proxyQualityGrade(score int) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 75:
		return "B"
	case score >= 60:
		return "C"
	case score >= 40:
		return "D"
	default:
		return "F"
	}
}

func proxyQualityOverallStatus(result *ProxyQualityCheckResult) string {
	if result == nil {
		return ""
	}
	if result.ChallengeCount > 0 {
		return "challenge"
	}
	if result.FailedCount > 0 {
		return "failed"
	}
	if result.WarnCount > 0 {
		return "warn"
	}
	if result.PassedCount > 0 {
		return "healthy"
	}
	return "failed"
}

func proxyQualityFirstCFRay(result *ProxyQualityCheckResult) string {
	if result == nil {
		return ""
	}
	for _, item := range result.Items {
		if item.CFRay != "" {
			return item.CFRay
		}
	}
	return ""
}

func proxyQualityBaseConnectivityPass(result *ProxyQualityCheckResult) bool {
	if result == nil {
		return false
	}
	for _, item := range result.Items {
		if item.Target == "base_connectivity" {
			return item.Status == "pass"
		}
	}
	return false
}

func (s *adminServiceImpl) saveProxyQualitySnapshot(ctx context.Context, proxyID int64, result *ProxyQualityCheckResult, exitInfo *ProxyExitInfo) {
	if result == nil {
		return
	}
	score := result.Score
	checkedAt := result.CheckedAt
	info := &ProxyLatencyInfo{
		Success:          proxyQualityBaseConnectivityPass(result),
		Message:          result.Summary,
		QualityStatus:    proxyQualityOverallStatus(result),
		QualityScore:     &score,
		QualityGrade:     result.Grade,
		QualitySummary:   result.Summary,
		QualityCheckedAt: &checkedAt,
		QualityCFRay:     proxyQualityFirstCFRay(result),
		UpdatedAt:        time.Now(),
	}
	if result.BaseLatencyMs > 0 {
		latency := result.BaseLatencyMs
		info.LatencyMs = &latency
	}
	if exitInfo != nil {
		info.IPAddress = exitInfo.IP
		info.Country = exitInfo.Country
		info.CountryCode = exitInfo.CountryCode
		info.Region = exitInfo.Region
		info.City = exitInfo.City
	}
	s.saveProxyLatency(ctx, proxyID, info)
}

func (s *adminServiceImpl) probeProxyLatency(ctx context.Context, proxy *Proxy) {
	if s.proxyProber == nil || proxy == nil {
		return
	}
	exitInfo, latencyMs, err := s.proxyProber.ProbeProxy(ctx, proxy.URL())
	if err != nil {
		s.saveProxyLatency(ctx, proxy.ID, &ProxyLatencyInfo{
			Success:   false,
			Message:   err.Error(),
			UpdatedAt: time.Now(),
		})
		return
	}

	latency := latencyMs
	s.saveProxyLatency(ctx, proxy.ID, &ProxyLatencyInfo{
		Success:     true,
		LatencyMs:   &latency,
		Message:     "Proxy is accessible",
		IPAddress:   exitInfo.IP,
		Country:     exitInfo.Country,
		CountryCode: exitInfo.CountryCode,
		Region:      exitInfo.Region,
		City:        exitInfo.City,
		UpdatedAt:   time.Now(),
	})
}

func (s *adminServiceImpl) attachProxyLatency(ctx context.Context, proxies []ProxyWithAccountCount) {
	if s.proxyLatencyCache == nil || len(proxies) == 0 {
		return
	}

	ids := make([]int64, 0, len(proxies))
	for i := range proxies {
		if proxies[i].ID > 0 {
			ids = append(ids, proxies[i].ID)
		}
	}
	if len(ids) == 0 {
		return
	}

	latencies, err := s.proxyLatencyCache.GetProxyLatencies(ctx, ids)
	if err != nil {
		logger.LegacyPrintf("service.admin", "Warning: load proxy latency cache failed: %v", err)
		return
	}

	for i := range proxies {
		info := latencies[proxies[i].ID]
		if info == nil {
			continue
		}
		if info.Success {
			proxies[i].LatencyStatus = "success"
			proxies[i].LatencyMs = info.LatencyMs
		} else {
			proxies[i].LatencyStatus = "failed"
		}
		proxies[i].LatencyMessage = info.Message
		proxies[i].IPAddress = info.IPAddress
		proxies[i].Country = info.Country
		proxies[i].CountryCode = info.CountryCode
		proxies[i].Region = info.Region
		proxies[i].City = info.City
		proxies[i].QualityStatus = info.QualityStatus
		proxies[i].QualityScore = info.QualityScore
		proxies[i].QualityGrade = info.QualityGrade
		proxies[i].QualitySummary = info.QualitySummary
		proxies[i].QualityChecked = info.QualityCheckedAt
	}
}

func (s *adminServiceImpl) saveProxyLatency(ctx context.Context, proxyID int64, info *ProxyLatencyInfo) {
	if s.proxyLatencyCache == nil || info == nil {
		return
	}

	merged := *info
	if latencies, err := s.proxyLatencyCache.GetProxyLatencies(ctx, []int64{proxyID}); err == nil {
		if existing := latencies[proxyID]; existing != nil {
			if merged.QualityCheckedAt == nil &&
				merged.QualityScore == nil &&
				merged.QualityGrade == "" &&
				merged.QualityStatus == "" &&
				merged.QualitySummary == "" &&
				merged.QualityCFRay == "" {
				merged.QualityStatus = existing.QualityStatus
				merged.QualityScore = existing.QualityScore
				merged.QualityGrade = existing.QualityGrade
				merged.QualitySummary = existing.QualitySummary
				merged.QualityCheckedAt = existing.QualityCheckedAt
				merged.QualityCFRay = existing.QualityCFRay
			}
		}
	}

	if err := s.proxyLatencyCache.SetProxyLatency(ctx, proxyID, &merged); err != nil {
		logger.LegacyPrintf("service.admin", "Warning: store proxy latency cache failed: %v", err)
	}
}
