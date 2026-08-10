package service

import "context"

// SelectAccountForModelWithExclusions adds the provider-neutral Key health
// protection layer while leaving the original scheduler untouched.
func (s *GatewayService) SelectAccountForModelWithExclusions(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*Account, error) {
	healthExcluded := s.upstreamHealthExcludedAccountIDs(ctx, groupID)
	if len(healthExcluded) == 0 {
		return s.selectAccountForModelWithExclusionsCore(ctx, groupID, sessionHash, requestedModel, excludedIDs)
	}
	effective := cloneExcludedAccountIDs(excludedIDs)
	if effective == nil {
		effective = make(map[int64]struct{}, len(healthExcluded))
	}
	for id := range healthExcluded {
		effective[id] = struct{}{}
	}
	account, err := s.selectAccountForModelWithExclusionsCore(ctx, groupID, sessionHash, requestedModel, effective)
	if err == nil {
		return account, nil
	}
	// Fail-open only when the health layer exhausted the group's candidates.
	if err == ErrNoAvailableAccounts {
		return s.selectAccountForModelWithExclusionsCore(ctx, groupID, sessionHash, requestedModel, cloneExcludedAccountIDs(excludedIDs))
	}
	return nil, err
}

func (s *GatewayService) SelectAccountWithLoadAwareness(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}, metadataUserID string, sub2apiUserID int64) (*AccountSelectionResult, error) {
	healthExcluded := s.upstreamHealthExcludedAccountIDs(ctx, groupID)
	if len(healthExcluded) == 0 {
		return s.selectAccountWithLoadAwarenessCore(ctx, groupID, sessionHash, requestedModel, excludedIDs, metadataUserID, sub2apiUserID)
	}
	effective := cloneExcludedAccountIDs(excludedIDs)
	if effective == nil {
		effective = make(map[int64]struct{}, len(healthExcluded))
	}
	for id := range healthExcluded {
		effective[id] = struct{}{}
	}
	result, err := s.selectAccountWithLoadAwarenessCore(ctx, groupID, sessionHash, requestedModel, effective, metadataUserID, sub2apiUserID)
	if err == nil && result != nil && result.Account != nil {
		return result, nil
	}
	if err == ErrNoAvailableAccounts || (err == nil && (result == nil || result.Account == nil)) {
		return s.selectAccountWithLoadAwarenessCore(ctx, groupID, sessionHash, requestedModel, cloneExcludedAccountIDs(excludedIDs), metadataUserID, sub2apiUserID)
	}
	return result, err
}

func (s *GatewayService) upstreamHealthExcludedAccountIDs(ctx context.Context, groupID *int64) map[int64]struct{} {
	if s == nil || s.accountRepo == nil {
		return nil
	}
	healthRegistry := GlobalUpstreamHealthRegistry()
	if !healthRegistry.HasTemporaryExclusions() {
		return nil
	}
	var accounts []Account
	var err error
	if groupID != nil {
		accounts, err = s.accountRepo.ListSchedulableByGroupID(ctx, *groupID)
	} else {
		accounts, err = s.accountRepo.ListSchedulableUngroupedByPlatform(ctx, PlatformAnthropic)
	}
	if err != nil {
		return nil
	}
	ids := make([]int64, 0, len(accounts))
	keyByAccount := make(map[int64]int64, len(accounts))
	for i := range accounts {
		if accounts[i].UpstreamKeyID == nil {
			continue
		}
		ids = append(ids, accounts[i].ID)
		keyByAccount[accounts[i].ID] = *accounts[i].UpstreamKeyID
	}
	keyIDs := make([]int64, 0, len(ids))
	for _, id := range ids {
		keyIDs = append(keyIDs, keyByAccount[id])
	}
	keyExcluded := healthRegistry.ExcludedKeyIDs(keyIDs)
	out := make(map[int64]struct{}, len(keyExcluded))
	for accountID, keyID := range keyByAccount {
		if _, ok := keyExcluded[keyID]; ok {
			out[accountID] = struct{}{}
		}
	}
	return out
}
