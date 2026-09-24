//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodexTicketCurrentTicketPostgres(t *testing.T) {
	const model = "gpt-6-astra"
	const ticket = `{"generation_id":"test-generation","state":"saved-state","cookie":"__oailb=saved"}`
	for _, test := range []struct {
		name, extra, want string
		deleted, missing  bool
	}{
		{name: "missing key", extra: `{}`, want: "null"},
		{name: "other model only", extra: `{"codex_turn_ticket:gpt-5.5":` + ticket + `}`, want: "null"},
		{name: "stored JSON null", extra: `{"codex_turn_ticket:gpt-6-astra":null}`, want: "null"},
		{name: "stored ticket", extra: `{"codex_turn_ticket:gpt-6-astra":` + ticket + `}`, want: ticket},
		{name: "deleted account", extra: `{"codex_turn_ticket:gpt-6-astra":` + ticket + `}`, deleted: true},
		{name: "missing account", extra: `{}`, missing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			var id int64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,extra)
				VALUES ('ticket-lookup-test','openai','oauth',$1::jsonb) RETURNING id`, test.extra).Scan(&id))
			t.Cleanup(func() {
				_, err := integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, id)
				require.NoError(t, err)
				_, err = integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE account_id=$1`, id)
				require.NoError(t, err)
			})
			if test.deleted {
				_, err := integrationDB.ExecContext(ctx, `UPDATE accounts SET deleted_at=NOW() WHERE id=$1`, id)
				require.NoError(t, err)
			}
			if test.missing {
				_, err := integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id=$1`, id)
				require.NoError(t, err)
			}
			repo := &codexTicketAttemptRepository{db: integrationDB}
			raw, err := repo.CurrentTicket(ctx, id, model)
			require.NoError(t, err, "a missing ticket must reach the gateway's no-ticket policy")
			if test.want == "" {
				require.Nil(t, raw)
			} else {
				require.JSONEq(t, test.want, string(raw))
			}
		})
	}
}
