package main

import "context"

var (
	ctxKeyAuthToken  = &contextKey{"authToken"}
	ctxKeyUser       = &contextKey{"user"}
	ctxKeyPlexAccess = &contextKey{"plexAccess"}
)

type contextKey struct {
	name string
}

func PlexAccessFromContext(ctx context.Context) *PlexAccess {
	value, _ := ctx.Value(ctxKeyPlexAccess).(*PlexAccess)
	return value
}

func ContextWithPlexAccess(ctx context.Context, access *PlexAccess) context.Context {
	return context.WithValue(ctx, ctxKeyPlexAccess, access)
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
