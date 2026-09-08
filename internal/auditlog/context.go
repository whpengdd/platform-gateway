package auditlog

import "context"

type ctxKey struct{}

func WithRecord(ctx context.Context, rec *Record) context.Context {
	if rec == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, rec)
}

func From(ctx context.Context) *Record {
	if ctx == nil {
		return nil
	}
	rec, _ := ctx.Value(ctxKey{}).(*Record)
	return rec
}
