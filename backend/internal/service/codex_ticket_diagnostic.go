package service

import "context"

type codexTicketDiagnosticTargetKey struct{}

func WithCodexTicketDiagnostic(ctx context.Context, accountID int64) context.Context {
	return context.WithValue(ctx, codexTicketDiagnosticTargetKey{}, accountID)
}

func isCodexTicketDiagnostic(ctx context.Context) bool {
	accountID, _ := ctx.Value(codexTicketDiagnosticTargetKey{}).(int64)
	return accountID > 0
}
