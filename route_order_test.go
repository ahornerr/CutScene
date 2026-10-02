package main

import (
	"net/http/httptest"
	"reflect"
	"runtime"
	"testing"

	"github.com/gofiber/fiber/v3"
)

// TestRouteHandlersAreRegisteredBeforeTheirMiddleware documents a Fiber v3
// behaviour that is easy to get backwards.
//
// Routes are registered as api.http.Get(path, handler, middleware), i.e. the
// handler appears FIRST. Fiber invokes the handlers of a route in reverse
// registration order, so the middleware still runs first and gates the handler.
//
// Registering these the intuitive way round (middleware first) would invert the
// chain and let unauthenticated callers reach every handler.
func TestRouteHandlersAreRegisteredBeforeTheirMiddleware(t *testing.T) {
	handlerRan := false
	app := fiber.New()
	app.Get("/probe",
		func(ctx fiber.Ctx) error {
			handlerRan = true
			return ctx.SendString("handler ran")
		},
		func(ctx fiber.Ctx) error {
			return renderAPIErrorCode(ctx, 401, "authentication_required", "authentication required")
		},
	)

	resp, err := app.Test(httptest.NewRequest("GET", "/probe", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if handlerRan {
		t.Error("handler ran before the middleware; unauthenticated callers would reach it")
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// TestUnauthenticatedRequestsNeverReachStateChangingHandlers checks the real
// router, where reaching a handler would mutate state rather than just read it.
func TestUnauthenticatedRequestsNeverReachStateChangingHandlers(t *testing.T) {
	application := &Application{}
	application.config.Plex.Host = "http://127.0.0.1:1"
	application.config.Plex.Token = "token"
	api, err := NewAPI(Config{}, application)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ method, path string }{
		{"POST", "/render-jobs"},
		{"DELETE", "/clips/any"},
		{"GET", "/clips"},
		{"GET", "/clips/any/download"},
		{"GET", "/library/source/1"},
		{"GET", "/subtitle-search"},
		{"GET", "/subtitle-search/index-jobs/current"},
		{"GET", "/render-jobs/any"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			resp, err := api.http.Test(httptest.NewRequest(tc.method, tc.path, nil))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 401 {
				t.Errorf("status = %d, want 401", resp.StatusCode)
			}
		})
	}

	// No queue was constructed, so nothing could have been enqueued or persisted.
	if application.renderJobs != nil {
		t.Error("a render queue was created during unauthenticated requests")
	}
}

// authMiddlewareNames identifies the two authentication middlewares so a test
// can tell them apart from endpoint handlers in a route's handler stack.
func authMiddlewareNames() map[string]bool {
	probe := &API{}
	return map[string]bool{
		handlerName(probe.authMiddleware):     true,
		handlerName(probe.authMiddlewareJSON): true,
	}
}

// handlerName returns the runtime name of a handler, which for methods carries
// the receiver type and method (for example ".../cutscene.(*API).authMiddleware-fm").
func handlerName(handler fiber.Handler) string {
	if handler == nil {
		return ""
	}
	fn := runtime.FuncForPC(reflect.ValueOf(handler).Pointer())
	if fn == nil {
		return ""
	}
	return fn.Name()
}

// TestProtectedRoutesRegisterHandlerBeforeMiddleware asserts the registration
// order on the real router, which response-level checks cannot detect because
// every handler also refuses unauthenticated callers itself.
//
// Fiber runs a route's handlers in reverse registration order, so the
// middleware must be listed SECOND in order to execute first. Reordering these
// to the intuitive [middleware, handler] would silently expose every endpoint.
func TestProtectedRoutesRegisterHandlerBeforeMiddleware(t *testing.T) {
	application := &Application{}
	application.config.Plex.Host = "http://127.0.0.1:1"
	api, err := NewAPI(Config{}, application)
	if err != nil {
		t.Fatal(err)
	}

	middleware := authMiddlewareNames()
	protected := map[string]bool{
		"/render-jobs": true, "/clips": true, "/library/search": true,
		"/subtitle-search": true, "/thumb": true, "/streams/:ratingKey": true,
		"/sessions": true, "/preview/:ratingKey/:from/:to": true,
	}

	checked := 0
	for _, route := range api.http.GetRoutes(true) {
		if !protected[route.Path] || len(route.Handlers) != 2 {
			continue
		}
		checked++
		// Fiber reverses a route's handler stack at registration, so the
		// middleware passed second is Handlers[0] and therefore runs first.
		if !middleware[handlerName(route.Handlers[0])] {
			t.Errorf("%s %s has %q at the front of its handler stack; the "+
				"authentication middleware must be first or unauthenticated callers "+
				"would reach the handler",
				route.Method, route.Path, handlerName(route.Handlers[0]))
		}
		if middleware[handlerName(route.Handlers[1])] {
			t.Errorf("%s %s has a middleware in the second handler position; "+
				"it would run after the endpoint",
				route.Method, route.Path)
		}
	}
	if checked == 0 {
		t.Fatal("no protected routes were inspected; the assertion proved nothing")
	}
}
