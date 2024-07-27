package main

import "context"

var (
	ctxKeyAuthToken = &contextKey{"authToken"}
	ctxKeyUser      = &contextKey{"user"}
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

func UserFromContext(ctx context.Context) *User {
	value, ok := ctx.Value(ctxKeyUser).(User)
	if !ok {
		return nil
	}
	return &value
}

func ContextWithUser(ctx context.Context, user User) context.Context {
	return context.WithValue(ctx, ctxKeyUser, user)
}
