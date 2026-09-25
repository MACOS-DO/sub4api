package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountRepositoryBPSCredentialConditionalState(t *testing.T) {
	exec := &recordingSQLExecutor{result: rowsAffectedResult(0)}
	repo := newAccountRepositoryWithSQL(nil, exec, nil)
	snapshot := service.OpenAIBPSCredentialSnapshot{AccessToken: "old-token", AccountID: "workspace"}
	now := time.Now().UTC()
	changed, err := repo.SetOpenAIBPSCredentialErrorIfMatch(context.Background(), 42, snapshot, service.OpenAIBPSCredentialState{Status: "revoked", ErrorCode: "token_revoked", ObservedAt: &now})
	require.NoError(t, err)
	require.False(t, changed)
	require.Len(t, exec.execQueries, 1)
	query := normalizeSQLWhitespace(exec.execQueries[0])
	require.Contains(t, query, "WITH updated AS (")
	require.Contains(t, query, "a.credentials->>'access_token' = $9")
	require.Contains(t, query, "a.credentials->>'chatgpt_account_id' = $10")
	require.Contains(t, query, "INSERT INTO scheduler_outbox")
	require.NotContains(t, query, "schedulable =")
	require.Contains(t, query, "CASE WHEN a.status = $3 THEN $4 ELSE a.status END")
	require.Equal(t, "old-token", exec.execArgs[0][8])
	require.Equal(t, "workspace", exec.execArgs[0][9])
	var payload map[string]any
	require.NoError(t, json.Unmarshal(exec.execArgs[0][1].([]byte), &payload))
	require.Equal(t, "revoked", payload["status"])
	require.Equal(t, "token_revoked", payload["error_code"])
	require.NotContains(t, string(exec.execArgs[0][1].([]byte)), "old-token")
	require.Equal(t, service.OpenAIBPSCredentialIdentity(snapshot), payload["credential_identity"])
}
