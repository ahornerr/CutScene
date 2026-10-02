package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
)

// TestSessionCookieIsHardened asserts the attributes of the cookie the service
// actually emits, using the real session store rather than a synthetic config.
//
// The session carries the caller's Plex auth token. Fiber's session middleware
// defaults CookieHTTPOnly and CookieSecure to false, so without explicit
// configuration the token-bearing cookie was readable from JavaScript and was
// sent over plain HTTP.
func TestSessionCookieIsHardened(t *testing.T) {
	app := fiber.New()
	app.Get("/probe", func(ctx fiber.Ctx) error {
		sess, err := store.Get(ctx)
		if err != nil {
			return err
		}
		sess.Set("probe", "value")
		if err := sess.Save(); err != nil {
			return err
		}
		return ctx.SendString("ok")
	})

	resp, err := app.Test(httptest.NewRequest("GET", "/probe", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	raw := resp.Header.Values("Set-Cookie")
	if len(raw) == 0 {
		t.Fatal("no session cookie was issued")
	}
	cookie := strings.Join(raw, "; ")

	lower := strings.ToLower(cookie)
	for _, want := range []string{"httponly", "samesite=lax", "path=/"} {
		if !strings.Contains(lower, want) {
			t.Errorf("session cookie %q is missing %q", cookie, want)
		}
	}
	// secureCookies defaults to false for plain-HTTP LAN deployments.
	if strings.Contains(lower, "secure") {
		t.Errorf("session cookie %q is Secure although secure cookies are disabled in this test", cookie)
	}
}
