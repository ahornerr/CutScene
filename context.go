package main

import "context"

var (
	ctxKeyAuthToken = &contextKey{"authToken"}
)

type contextKey struct {
	name string
}

func AuthTokenFromContext(ctx context.Context) *string {
	value, ok := ctx.Value(ctxKeyAuthToken).(string)
	if !ok {
		return nil
	}
	return &value
}

func ContextWithAuthToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, ctxKeyAuthToken, token)
}
