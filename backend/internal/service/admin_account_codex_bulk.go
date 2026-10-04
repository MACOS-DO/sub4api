package service

import (
	"context"
	"maps"
)

// Gateway-owned accounts must pass through the normal state coordinator even
// for bulk edits. A raw JSONB merge must never persist upstream credentials or
// change a proxy without closing the synchronization gate.
func (s *adminServiceImpl) bulkUpdateCodexAccounts(ctx context.Context, input *BulkUpdateAccountsInput) (*BulkUpdateAccountsResult, bool, error) {
	if s.codexGateway == nil || s.codexGateway.bindings == nil {
		return nil, false, nil
	}
	accounts, err := s.accountRepo.GetByIDs(ctx, input.AccountIDs)
	if err != nil {
		return nil, true, err
	}
	managed := map[int64]*Account{}
	for _, account := range accounts {
		if account.IsOpenAICodex() {
			managed[account.ID] = account
		}
	}
	if len(managed) == 0 {
		return nil, false, nil
	}
	result := &BulkUpdateAccountsResult{SuccessIDs: []int64{}, FailedIDs: []int64{}, Results: []BulkUpdateAccountResult{}}
	legacyIDs := make([]int64, 0)
	for _, id := range input.AccountIDs {
		account := managed[id]
		if account == nil {
			legacyIDs = append(legacyIDs, id)
			continue
		}
		credentials := maps.Clone(account.Credentials)
		if credentials == nil {
			credentials = map[string]any{}
		}
		maps.Copy(credentials, input.Credentials)
		extra := maps.Clone(account.Extra)
		if extra == nil {
			extra = map[string]any{}
		}
		maps.Copy(extra, input.Extra)
		_, updateErr := s.UpdateAccount(ctx, id, &UpdateAccountInput{Name: input.Name, ProxyID: input.ProxyID, Concurrency: input.Concurrency, Priority: input.Priority, RateMultiplier: input.RateMultiplier, LoadFactor: input.LoadFactor, Status: input.Status, GroupIDs: input.GroupIDs, Credentials: credentials, Extra: extra})
		if updateErr == nil && input.Schedulable != nil {
			updateErr = s.accountRepo.SetSchedulable(ctx, id, *input.Schedulable)
		}
		entry := BulkUpdateAccountResult{AccountID: id, Success: updateErr == nil}
		if updateErr != nil {
			entry.Error = updateErr.Error()
			result.Failed++
			result.FailedIDs = append(result.FailedIDs, id)
		} else {
			result.Success++
			result.SuccessIDs = append(result.SuccessIDs, id)
		}
		result.Results = append(result.Results, entry)
	}
	if len(legacyIDs) > 0 {
		legacyInput := *input
		legacyInput.AccountIDs = legacyIDs
		legacyInput.Filters = nil
		legacy, err := s.BulkUpdateAccounts(ctx, &legacyInput)
		if err != nil {
			return nil, true, err
		}
		result.Success += legacy.Success
		result.Failed += legacy.Failed
		result.SuccessIDs = append(result.SuccessIDs, legacy.SuccessIDs...)
		result.FailedIDs = append(result.FailedIDs, legacy.FailedIDs...)
		result.Results = append(result.Results, legacy.Results...)
		result.LongContextInheritedCount = legacy.LongContextInheritedCount
	}
	return result, true, nil
}
