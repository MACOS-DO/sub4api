package repository

import (
	"context"
	"encoding/json"

	"github.com/MACOS-DO/sub4api/internal/service"
)

var _ service.OpenAIBPSCredentialStateRepository = (*accountRepository)(nil)

// The diagnosis and scheduler notification commit together, only for the exact
// credentials used by the failed request. Administrative pause/disable is retained.
func (r *accountRepository) SetOpenAIBPSCredentialErrorIfMatch(ctx context.Context, id int64, snapshot service.OpenAIBPSCredentialSnapshot, state service.OpenAIBPSCredentialState) (bool, error) {
	if snapshot.AccessToken == "" || snapshot.AccountID == "" {
		return false, nil
	}
	payload, err := json.Marshal(struct {
		service.OpenAIBPSCredentialState
		CredentialIdentity string `json:"credential_identity"`
	}{state, service.OpenAIBPSCredentialIdentity(snapshot)})
	if err != nil {
		return false, err
	}
	message := service.OpenAIBPSCredentialErrorMessage(state)
	result, err := r.sql.ExecContext(ctx, `
 WITH updated AS (
  UPDATE accounts AS a
  SET extra = jsonb_set(COALESCE(a.extra,'{}'::jsonb), ARRAY[$1]::text[],
       $2::jsonb || jsonb_build_object('managed_status_error',
         a.status = $3 OR (a.status = $4 AND COALESCE(a.extra->$1->>'managed_status_error' = 'true', a.error_message IN ('BPS authentication failed; replace access_token', 'BPS access token expired; replace it manually', 'BPS access token revoked; replace access_token')))), true),
      status = CASE WHEN a.status = $3 THEN $4 ELSE a.status END,
      error_message = CASE WHEN a.status = $3 OR
        (a.status = $4 AND COALESCE(a.extra->$1->>'managed_status_error' = 'true', a.error_message IN ('BPS authentication failed; replace access_token', 'BPS access token expired; replace it manually', 'BPS access token revoked; replace access_token')))
        THEN $5 ELSE a.error_message END,
      updated_at = NOW()
  WHERE a.id = $6 AND a.deleted_at IS NULL AND a.platform = $7 AND a.type = $8
    AND a.credentials->>'access_token' = $9
    AND a.credentials->>'chatgpt_account_id' = $10
  RETURNING a.id
 )
 INSERT INTO scheduler_outbox (event_type,account_id,group_id,payload)
 SELECT $11,updated.id,NULL,NULL FROM updated
 `, service.OpenAIBPSCredentialStateExtraKey, payload, service.StatusActive, service.StatusError, message, id, service.PlatformOpenAIBPS, service.AccountTypeOAuth, snapshot.AccessToken, snapshot.AccountID, service.SchedulerOutboxEventAccountChanged)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, id)
	return true, nil
}
