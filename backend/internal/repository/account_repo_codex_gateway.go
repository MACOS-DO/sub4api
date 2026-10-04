package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	dbent "github.com/MACOS-DO/sub4api/ent"
	infraerrors "github.com/MACOS-DO/sub4api/internal/pkg/errors"
	"github.com/MACOS-DO/sub4api/internal/service"
	"github.com/lib/pq"
)

var _ service.CodexBindingRepository = (*accountRepository)(nil)

func insertCodexBinding(ctx context.Context, client *dbent.Client, account *service.Account) error {
	b := account.GatewayBinding
	if !account.IsOpenAICodex() || account.IsShadow() {
		return nil
	}
	if b == nil || b.GatewayID == "" || b.CreationKey == "" {
		return errors.New("OpenAI Codex creation requires a durable binding")
	}
	b.AccountID = account.ID
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = client.ExecContext(ctx, `INSERT INTO public.openai_codex_account_bindings (account_id,gateway_id,creation_key,record) VALUES ($1,$2,$3,$4::jsonb)`, account.ID, b.GatewayID, b.CreationKey, raw)
	account.Gateway = b.Projection()
	return err
}

func applyCodexBindingUpdate(ctx context.Context, client *dbent.Client) error {
	update := service.CodexBindingUpdateFromContext(ctx)
	if update == nil {
		return nil
	}
	raw, err := json.Marshal(update.Binding)
	if err != nil {
		return err
	}
	result, err := client.ExecContext(ctx, `UPDATE public.openai_codex_account_bindings SET record=$2::jsonb,updated_at=NOW() WHERE account_id=$1 AND (record->>'config_version')::bigint=$3 AND record->>'operation_id'=$4`, update.Binding.AccountID, raw, update.ExpectedVersion, update.ExpectedOperation)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("OpenAI Codex binding changed concurrently")
	}
	return nil
}

func (r *accountRepository) GetCodexBinding(ctx context.Context, id int64) (*service.CodexAccountBinding, error) {
	return r.readCodexBinding(ctx, `SELECT record FROM public.openai_codex_account_bindings WHERE account_id=$1`, id)
}

func (r *accountRepository) FindCodexBindingByCreationKey(ctx context.Context, key string) (*service.CodexAccountBinding, error) {
	return r.readCodexBinding(ctx, `SELECT b.record FROM public.openai_codex_account_bindings b JOIN accounts a ON a.id=b.account_id WHERE b.creation_key=$1 AND a.deleted_at IS NULL`, key)
}

func (r *accountRepository) DeleteCodexBinding(ctx context.Context, id int64) error {
	_, err := r.sql.ExecContext(ctx, `DELETE FROM public.openai_codex_account_bindings WHERE account_id=$1`, id)
	return err
}

// The lookup key is an actor-scoped digest; only non-secret receipts are indexed.
func (r *accountRepository) FindCodexBindingsByOperation(ctx context.Context, key string) ([]*service.CodexAccountBinding, error) {
	rows, err := r.sql.QueryContext(ctx, `SELECT b.record FROM public.openai_codex_account_bindings b JOIN accounts a ON a.id=b.account_id WHERE a.deleted_at IS NULL AND (b.record->'operations') ? $1 ORDER BY b.account_id`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*service.CodexAccountBinding
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var b service.CodexAccountBinding
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, err
		}
		result = append(result, &b)
	}
	return result, rows.Err()
}

func (r *accountRepository) readCodexBinding(ctx context.Context, query string, arg any) (*service.CodexAccountBinding, error) {
	var raw []byte
	rows, err := r.sql.QueryContext(ctx, query, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	if err = rows.Scan(&raw); err != nil {
		return nil, err
	}
	var b service.CodexAccountBinding
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	return &b, nil
}

func (r *accountRepository) SaveCodexBinding(ctx context.Context, b *service.CodexAccountBinding, version int64, operation string) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	result, err := tx.Client().ExecContext(ctx, `UPDATE public.openai_codex_account_bindings SET record=$2::jsonb,updated_at=NOW() WHERE account_id=$1 AND (record->>'config_version')::bigint=$3 AND record->>'operation_id'=$4`, b.AccountID, raw, version, operation)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("OpenAI Codex binding changed concurrently")
	}
	if err = enqueueSchedulerOutbox(ctx, tx.Client(), service.SchedulerOutboxEventAccountChanged, &b.AccountID, nil, nil); err != nil {
		return err
	}
	// A parent transition must retire or refresh its shadow projections too.
	rows, err := tx.Client().QueryContext(ctx, `SELECT id FROM accounts WHERE parent_account_id=$1 AND deleted_at IS NULL`, b.AccountID)
	if err != nil {
		return err
	}
	var shadows []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		shadows = append(shadows, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, id := range shadows {
		if err = enqueueSchedulerOutbox(ctx, tx.Client(), service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	r.syncSchedulerAccountSnapshot(ctx, b.AccountID)
	for _, id := range shadows {
		r.syncSchedulerAccountSnapshot(ctx, id)
	}
	return nil
}

func (r *accountRepository) ListCodexBindingAccountIDs(ctx context.Context) ([]int64, error) {
	rows, err := r.sql.QueryContext(ctx, `SELECT b.account_id FROM public.openai_codex_account_bindings b JOIN accounts a ON a.id=b.account_id WHERE a.deleted_at IS NULL ORDER BY b.account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func (r *accountRepository) WithCodexBindingLock(ctx context.Context, key string, fn func(context.Context) error) error {
	db, ok := r.sql.(*sql.DB)
	if !ok {
		return errors.New("OpenAI Codex requires PostgreSQL account locking")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	locked := false
	defer func() {
		if locked {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := conn.ExecContext(releaseCtx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, "sub4api:openai_codex:"+key); err != nil {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}
		_ = conn.Close()
	}()
	if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, "sub4api:openai_codex:"+key).Scan(&locked); err != nil {
		// PostgreSQL may have acquired the session lock before cancellation.
		// Never return an ambiguously locked session to the pool.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return err
	}
	if !locked {
		return infraerrors.Conflict("CODEX_OPERATION_PENDING", "OpenAI Codex account operation is in progress")
	}
	return fn(ctx)
}

func (r *accountRepository) hydrateCodexBindings(ctx context.Context, accounts []service.Account) error {
	ids := make([]int64, 0)
	for _, a := range accounts {
		if a.IsOpenAICodex() {
			id := a.ID
			if a.ParentAccountID != nil {
				id = *a.ParentAccountID
			}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT record FROM public.openai_codex_account_bindings WHERE account_id=ANY($1)`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()
	bindings := map[int64]*service.CodexAccountBinding{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		var b service.CodexAccountBinding
		if err = json.Unmarshal(raw, &b); err != nil {
			return err
		}
		bindings[b.AccountID] = &b
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for i := range accounts {
		a := &accounts[i]
		if !a.IsOpenAICodex() {
			continue
		}
		id := a.ID
		if a.ParentAccountID != nil {
			id = *a.ParentAccountID
		}
		b := bindings[id]
		a.GatewayBinding = b
		a.Gateway = b.Projection()
	}
	return nil
}

func codexAccountLockKey(id int64) string { return "account:" + strconv.FormatInt(id, 10) }
