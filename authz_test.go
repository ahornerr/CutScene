package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v3"
	"testing"
)

// stubPlexTV installs a plexTVHTTPClient that answers the two plex.tv endpoints
// used by the authorization gate: the account lookup and the server user list.
func stubPlexTV(t *testing.T, accountJSON, usersXML string, accountStatus int) {
	t.Helper()
	original := plexTVHTTPClient
	t.Cleanup(func() { plexTVHTTPClient = original })

	plexTVHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var status int
		var body string
		switch {
		case strings.HasSuffix(req.URL.Path, "/users/account.json"):
			status, body = accountStatus, accountJSON
		case strings.HasSuffix(req.URL.Path, "/api/users"):
			status, body = http.StatusOK, usersXML
		case strings.HasSuffix(req.URL.Path, "/api/v2/pins.json"):
			status, body = accountStatus, accountJSON
		default:
			t.Errorf("unexpected plex.tv request: %s", req.URL)
			status, body = http.StatusNotFound, ""
		}
		return &http.Response{
			StatusCode: status,
			Body:       nopCloser{strings.NewReader(body)},
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }

// usersXMLFor renders a plex.tv /api/users payload granting the given user ids
// access to machineIdentifier.
func usersXMLFor(machineIdentifier string, userIDs ...string) string {
	var b strings.Builder
	b.WriteString(`<MediaContainer machineIdentifier="` + machineIdentifier + `">`)
	for _, id := range userIDs {
		b.WriteString(`<User id="` + id + `" username="u` + id + `">`)
		b.WriteString(`<Server id="1" machineIdentifier="` + machineIdentifier + `" owned="1" pending="0"/>`)
		b.WriteString(`</User>`)
	}
	b.WriteString(`</MediaContainer>`)
	return b.String()
}

// TestGetValidatedUserAuthorizesOwnerAndInvitees covers the gate that decides
// whether a Plex account may use CutScene at all. It had no coverage, so the
// allow-list behaviour was entirely unverified.
func TestGetValidatedUserAuthorizesOwnerAndInvitees(t *testing.T) {
	const machine = "machine-abc"

	tests := []struct {
		name      string
		ownerUUID string
		ownerMail string
		account   string
		invited   []string
		sharedOn  string // machine identifier the user list shares them on
		wantErr   bool
	}{
		{
			name:      "server owner is authorized",
			ownerUUID: "owner-uuid",
			account:   `{"user":{"id":1,"uuid":"owner-uuid","title":"Owner","email":"owner@example.com"}}`,
		},
		{
			name:      "owner matched by email when uuid is unset",
			ownerMail: "owner@example.com",
			account:   `{"user":{"id":1,"uuid":"","title":"Owner","email":"owner@example.com"}}`,
		},
		{
			name:    "invited user is authorized",
			account: `{"user":{"id":7,"uuid":"guest-uuid","title":"Guest","email":"guest@example.com"}}`,
			invited: []string{"7"},
		},
		{
			// The user exists on plex.tv and is shared with a server, but not
			// this one. Sharing with some other server must not grant access.
			name:     "user shared on a different server is rejected",
			account:  `{"user":{"id":7,"uuid":"guest-uuid","title":"Guest","email":"guest@example.com"}}`,
			invited:  []string{"7"},
			sharedOn: "some-other-machine",
			wantErr:  true,
		},
		{
			name:    "uninvited user is rejected",
			account: `{"user":{"id":9,"uuid":"stranger-uuid","title":"Stranger","email":"stranger@example.com"}}`,
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sharedOn := test.sharedOn
			if sharedOn == "" {
				sharedOn = machine
			}
			stubPlexTV(t, test.account, usersXMLFor(sharedOn, test.invited...), http.StatusOK)

			app := &Application{}
			app.plexTv = NewPlexTV("server-token")
			app.machineIdentifier = machine
			app.ownerUUID = test.ownerUUID
			app.ownerEmail = test.ownerMail

			user, err := app.GetValidatedUser(ContextWithAuthToken(context.Background(), "user-token"))
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected the user to be rejected, got %+v", user)
				}
				if !errors.Is(err, ErrUserNotInvited) {
					t.Fatalf("error = %v, want ErrUserNotInvited", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetValidatedUser returned %v", err)
			}
			if user == nil {
				t.Fatal("GetValidatedUser returned a nil user")
			}
		})
	}
}

// TestGetValidatedUserPropagatesUpstreamFailures ensures an infrastructure
// failure is not mistaken for "not invited", which would surface as a confusing
// rejection rather than a retryable error.
func TestGetValidatedUserPropagatesUpstreamFailures(t *testing.T) {
	stubPlexTV(t, "not json", "", http.StatusInternalServerError)

	app := &Application{}
	app.plexTv = NewPlexTV("server-token")
	app.machineIdentifier = "machine-abc"

	if _, err := app.GetValidatedUser(ContextWithAuthToken(context.Background(), "user-token")); err == nil {
		t.Fatal("expected an error when the account lookup fails")
	} else if errors.Is(err, ErrUserNotInvited) {
		t.Fatalf("upstream failure reported as ErrUserNotInvited: %v", err)
	}
}

// TestAuthUrlBuildsPlexRedirectFromConfiguredDomain covers the login entry
// point, which had no coverage. The redirect carries Plex's forwardUrl, so it
// must come from configuration rather than anything the caller can influence.
func TestAuthUrlBuildsPlexRedirectFromConfiguredDomain(t *testing.T) {
	stubPlexTV(t, `{"id":4242,"code":"PINCODE"}`, "", http.StatusOK)

	api := &API{config: Config{}}
	api.config.API.Domain = "https://cutscene.example.com"

	app := fiber.New()
	app.Get("/authUrl", api.authUrl)

	// A caller-supplied forwardUrl must be ignored: Plex sends the browser back
	// to this value after sign-in, so letting the request influence it would be
	// an open redirect.
	resp, err := app.Test(httptest.NewRequest("GET", "/authUrl?forwardUrl=https://evil.example", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != fiber.StatusFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusFound)
	}
	location := resp.Header.Get("Location")
	if !strings.HasPrefix(location, "https://app.plex.tv/auth#?") {
		t.Fatalf("redirect does not target Plex auth: %q", location)
	}
	fragment := strings.TrimPrefix(location, "https://app.plex.tv/auth#?")
	values, err := url.ParseQuery(fragment)
	if err != nil {
		t.Fatalf("parsing redirect fragment %q: %v", fragment, err)
	}
	if got := values.Get("forwardUrl"); got != "https://cutscene.example.com" {
		t.Errorf("forwardUrl = %q, want the configured domain (a caller-supplied value must be ignored)", got)
	}
	if got := values.Get("code"); got != "PINCODE" {
		t.Errorf("code = %q, want PINCODE", got)
	}
	if got := values.Get("clientID"); got == "" {
		t.Error("clientID is missing from the Plex redirect")
	}
	if got := values.Get("context[device][product]"); got != productCutScene {
		t.Errorf("product = %q, want %q", got, productCutScene)
	}
}

// TestAuthUrlSurfacesPinFailure ensures a Plex PIN failure is returned rather
// than redirecting the browser to a broken authentication URL.
func TestAuthUrlSurfacesPinFailure(t *testing.T) {
	stubPlexTV(t, `{"error":"nope"}`, "", http.StatusInternalServerError)

	api := &API{config: Config{}}
	api.config.API.Domain = "https://cutscene.example.com"

	app := fiber.New()
	app.Get("/authUrl", api.authUrl)

	resp, err := app.Test(httptest.NewRequest("GET", "/authUrl", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == fiber.StatusFound {
		t.Fatalf("expected failure, got redirect to %q", resp.Header.Get("Location"))
	}
}

// authMiddlewareApp mounts authMiddlewareJSON with app.Use, the idiomatic
// middleware form, and records whether the protected handler was reached.
func authMiddlewareApp(api *API, reached *bool) *fiber.App {
	app := fiber.New()
	app.Use(api.authMiddlewareJSON)
	app.Get("/protected", func(ctx fiber.Ctx) error {
		if reached != nil {
			*reached = true
		}
		return ctx.SendString("ok")
	})
	return app
}

// TestAuthMiddlewareJSONGuardsProtectedRoutes covers the middleware that gates
// every JSON API route. It had no coverage.
func TestAuthMiddlewareJSONGuardsProtectedRoutes(t *testing.T) {
	t.Run("no credentials are rejected", func(t *testing.T) {
		api := &API{}
		api.validateUser = func(context.Context) (*User, error) {
			t.Error("user validation must not run without credentials")
			return nil, nil
		}
		reached := false
		resp, err := authMiddlewareApp(api, &reached).Test(httptest.NewRequest("GET", "/protected", nil))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
		}
		if reached {
			t.Fatal("handler ran despite missing credentials")
		}
	})

	t.Run("rejected user is unauthorized", func(t *testing.T) {
		api := &API{}
		api.validateUser = func(context.Context) (*User, error) {
			return nil, ErrUserNotInvited
		}
		reached := false
		resp, err := authMiddlewareApp(api, &reached).Test(httptest.NewRequest("GET", "/protected", nil))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
		}
		if reached {
			t.Fatal("handler ran for a rejected user")
		}
	})

	t.Run("user without a stable id is unauthorized", func(t *testing.T) {
		api := &API{}
		api.validateUser = func(context.Context) (*User, error) {
			// A user with no UUID cannot own durable state, so the middleware
			// must refuse rather than admit them.
			return &User{Email: "nobody@example.com"}, nil
		}
		reached := false
		resp, err := authMiddlewareApp(api, &reached).Test(httptest.NewRequest("GET", "/protected", nil))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
		}
		if reached {
			t.Fatal("handler ran for a user without an id")
		}
	})

	t.Run("pre-authenticated context short circuits validation", func(t *testing.T) {
		api := &API{}
		api.validateUser = func(context.Context) (*User, error) {
			t.Error("validation must be skipped when the context is already authenticated")
			return nil, nil
		}
		app := fiber.New()
		app.Use(func(ctx fiber.Ctx) error {
			ctx.SetUserContext(ContextWithUser(
				ContextWithAuthToken(ctx.UserContext(), "tok"), User{Uuid: "user-a"}))
			return ctx.Next()
		})
		app.Use(api.authMiddlewareJSON)
		app.Get("/protected", func(ctx fiber.Ctx) error { return ctx.SendString("ok") })

		resp, err := app.Test(httptest.NewRequest("GET", "/protected", nil))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
	})
}
