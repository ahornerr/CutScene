<?php



var (
	ctxKeyAuthToken  = &contextKey{"authToken"}
	ctxKeyUser       = &contextKey{"user"}
	ctxKeyPlexAccess = &contextKey{"plexAccess"}
)

class contextKey {    public $name;
}

function PlexAccessFromContext($$ctx->Context) {list($value, $_) = $ctx->Value(ctxKeyPlexAccess).(*PlexAccess)
	return value
}

function ContextWithPlexAccess($$ctx->Context, $access) {
	return $context->WithValue(ctx, ctxKeyPlexAccess, access)
}

function AuthTokenFromContext($$ctx->Context) {list($value, $ok) = $ctx->Value(ctxKeyAuthToken).(string)
	if !ok {
		return null
	}
	return &value
}

function ContextWithAuthToken($$ctx->Context, $token) {
	return $context->WithValue(ctx, ctxKeyAuthToken, token)
}

function UserFromContext($$ctx->Context) {list($value, $ok) = $ctx->Value(ctxKeyUser).(User)
	if !ok {
		return null
	}
	return &value
}

function ContextWithUser($$ctx->Context, $user) {
	return $context->WithValue(ctx, ctxKeyUser, user)
}
