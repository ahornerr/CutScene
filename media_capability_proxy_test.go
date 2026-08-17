package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPlexCapabilityProxyForwardsHeaderAndRangeAndRevokes(t *testing.T) {
	const resourceToken = "resource-secret"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plex-Token") != resourceToken {
			t.Errorf("upstream token = %q", r.Header.Get("X-Plex-Token"))
		}
		if r.Header.Get("Range") != "bytes=2-5" {
			t.Errorf("upstream range = %q", r.Header.Get("Range"))
		}
		if r.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("upstream accept-encoding = %q", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Range", "bytes 2-5/6")
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "cdef")
	}))
	defer upstream.Close()
	resolver, err := NewPlexResourceResolver(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := newPlexCapabilityProxy(resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	access := &PlexAccess{baseOrigin: upstream.URL, secretToken: resourceToken, accountToken: "account-secret", callerUUID: "caller", machineIdentifier: "machine"}
	capURL, release, err := proxy.Issue(context.Background(), access, "/library/parts/1/file")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(capURL, resourceToken) || strings.Contains(capURL, access.accountToken) || strings.Contains(capURL, upstream.URL) {
		t.Fatalf("capability URL leaked secret/origin: %q", capURL)
	}
	req, _ := http.NewRequest(http.MethodGet, capURL, nil)
	req.Header.Set("Range", "bytes=2-5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(body) != "cdef" || resp.Header.Get("Content-Range") != "bytes 2-5/6" {
		t.Fatalf("proxy response status=%d body=%q range=%q", resp.StatusCode, body, resp.Header.Get("Content-Range"))
	}
	release()
	resp, err = http.Get(capURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoked capability status = %d", resp.StatusCode)
	}
}

func issueTestMediaCapability(t *testing.T, upstreamURL string) (*plexCapabilityProxy, string, func()) {
	t.Helper()
	resolver, err := NewPlexResourceResolver(upstreamURL)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := newPlexCapabilityProxy(resolver)
	if err != nil {
		t.Fatal(err)
	}
	access := &PlexAccess{baseOrigin: upstreamURL, secretToken: "resource", accountToken: "account", callerUUID: "caller", machineIdentifier: "machine"}
	capURL, release, err := proxy.Issue(context.Background(), access, "/media")
	if err != nil {
		proxy.Close()
		t.Fatal(err)
	}
	return proxy, capURL, release
}

func TestPlexCapabilityProxyRejectsMalformedAndIgnoredRanges(t *testing.T) {
	var requests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Length", "4")
		_, _ = io.WriteString(w, "body")
	}))
	defer upstream.Close()
	proxy, capURL, release := issueTestMediaCapability(t, upstream.URL)
	defer proxy.Close()
	defer release()
	for _, value := range []string{"bytes=-4", "bytes=0-1,bytes=2-3"} {
		req, _ := http.NewRequest(http.MethodGet, capURL, nil)
		req.Header.Set("Range", value)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
			t.Fatalf("range %q status=%d", value, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, capURL, nil)
	req.Header.Set("Range", "bytes=0-3")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || requests != 1 {
		t.Fatalf("ignored range status=%d upstream requests=%d", resp.StatusCode, requests)
	}
}

func TestPlexCapabilityProxyAcceptsValidatedUnsatisfiedRange(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes */6")
		w.Header().Set("Content-Length", "9")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		_, _ = io.WriteString(w, "not-valid")
	}))
	defer upstream.Close()
	proxy, capURL, release := issueTestMediaCapability(t, upstream.URL)
	defer proxy.Close()
	defer release()
	req, _ := http.NewRequest(http.MethodGet, capURL, nil)
	req.Header.Set("Range", "bytes=9-12")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable || resp.Header.Get("Content-Range") != "bytes */6" || resp.Header.Get("Content-Length") != "0" || readErr != nil || len(body) != 0 {
		t.Fatalf("416 response status=%d range=%q length=%q body=%q readErr=%v", resp.StatusCode, resp.Header.Get("Content-Range"), resp.Header.Get("Content-Length"), body, readErr)
	}
}

func TestValidateMediaRangeClampsBoundedEndToResourceTotal(t *testing.T) {
	response := &http.Response{StatusCode: http.StatusPartialContent, Header: http.Header{
		"Content-Range":  []string{"bytes 2-4/5"},
		"Content-Length": []string{"3"},
	}}
	length, err := validateMediaUpstreamResponse(response, &mediaByteRange{start: 2, end: 99, hasEnd: true})
	if err != nil || length != 3 {
		t.Fatalf("clamped range length=%d err=%v", length, err)
	}
}

func TestValidateMediaRangeRejectsInvalidUnsatisfiedResponses(t *testing.T) {
	tests := []struct {
		name           string
		requestedStart int64
		contentRange   string
	}{
		{"start within resource", 4, "bytes */5"},
		{"malformed header", 9, "bytes * /5"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := &http.Response{StatusCode: http.StatusRequestedRangeNotSatisfiable, Header: http.Header{"Content-Range": []string{test.contentRange}}}
			if _, err := validateMediaUpstreamResponse(response, &mediaByteRange{start: test.requestedStart, end: test.requestedStart, hasEnd: true}); err == nil {
				t.Fatal("invalid 416 response accepted")
			}
		})
	}
}

func TestPlexCapabilityProxyExpiresAndCancels(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	resolver, _ := NewPlexResourceResolver(upstream.URL)
	proxy, err := newPlexCapabilityProxy(resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	proxy.ttl = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	access := &PlexAccess{baseOrigin: upstream.URL, secretToken: "resource", accountToken: "account", callerUUID: "caller", machineIdentifier: "machine"}
	capURL, _, err := proxy.Issue(ctx, access, "/identity")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	time.Sleep(10 * time.Millisecond)
	resp, err := http.Get(capURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cancelled capability status = %d", resp.StatusCode)
	}
}

func TestPlexCapabilityProxyTrueExpiryRevokesCapability(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	resolver, _ := NewPlexResourceResolver(upstream.URL)
	proxy, err := newPlexCapabilityProxy(resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	proxy.ttl = 20 * time.Millisecond
	access := &PlexAccess{baseOrigin: upstream.URL, secretToken: "resource", accountToken: "account", callerUUID: "caller", machineIdentifier: "machine"}
	capURL, _, err := proxy.Issue(context.Background(), access, "/identity")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	response, err := http.Get(capURL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("expired capability status = %d", response.StatusCode)
	}
}

func TestPlexCapabilityProxyReleaseCancelsActiveTransfer(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer upstream.Close()
	resolver, _ := NewPlexResourceResolver(upstream.URL)
	proxy, err := newPlexCapabilityProxy(resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	access := &PlexAccess{baseOrigin: upstream.URL, secretToken: "resource", accountToken: "account", callerUUID: "caller", machineIdentifier: "machine"}
	capURL, release, err := proxy.Issue(context.Background(), access, "/stream")
	if err != nil {
		t.Fatal(err)
	}
	responseDone := make(chan struct{})
	go func() {
		response, _ := http.Get(capURL)
		if response != nil {
			response.Body.Close()
		}
		close(responseDone)
	}()
	<-started
	release()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("release did not cancel active upstream transfer")
	}
	select {
	case <-responseDone:
	case <-time.After(time.Second):
		t.Fatal("proxy request did not finish after release")
	}
}

func TestPlexCapabilityProxyCloseCancelsTransfersAndDoesNotHang(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	defer upstream.Close()
	resolver, _ := NewPlexResourceResolver(upstream.URL)
	proxy, err := newPlexCapabilityProxy(resolver)
	if err != nil {
		t.Fatal(err)
	}
	access := &PlexAccess{baseOrigin: upstream.URL, secretToken: "resource", accountToken: "account", callerUUID: "caller", machineIdentifier: "machine"}
	capURL, _, err := proxy.Issue(context.Background(), access, "/stream")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		response, _ := http.Get(capURL)
		if response != nil {
			response.Body.Close()
		}
	}()
	<-started
	closed := make(chan error, 1)
	go func() { closed <- proxy.Close() }()
	select {
	case err := <-closed:
		if err != nil && !strings.Contains(err.Error(), "Server closed") {
			t.Fatalf("proxy close error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy Close hung on active transfer")
	}
}

func TestPlexCapabilityProxyReleaseRemovesLeaseAndLongBoundedTTL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	resolver, _ := NewPlexResourceResolver(upstream.URL)
	proxy, err := newPlexCapabilityProxy(resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	proxy.ttl = time.Millisecond
	access := &PlexAccess{baseOrigin: upstream.URL, secretToken: "resource", accountToken: "account", callerUUID: "caller", machineIdentifier: "machine"}
	capURL, release, err := proxy.IssueWithTTL(context.Background(), access, "/media", 3*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	proxy.mu.Lock()
	if len(proxy.items) != 1 {
		t.Fatal("capability lease was not installed")
	}
	proxy.mu.Unlock()
	response, err := http.Get(capURL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("long bounded capability status = %d", response.StatusCode)
	}
	release()
	time.Sleep(10 * time.Millisecond)
	proxy.mu.Lock()
	remaining := len(proxy.items)
	proxy.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("released lease remains: %d", remaining)
	}
}

func TestPlexCapabilityProxyParentDeadlineCancelsLease(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	resolver, _ := NewPlexResourceResolver(upstream.URL)
	proxy, _ := newPlexCapabilityProxy(resolver)
	defer proxy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	access := &PlexAccess{baseOrigin: upstream.URL, secretToken: "resource", accountToken: "account", callerUUID: "caller", machineIdentifier: "machine"}
	_, _, err := proxy.IssueWithTTL(ctx, access, "/identity", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	proxy.mu.Lock()
	remaining := len(proxy.items)
	proxy.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("deadline lease remains: %d", remaining)
	}
}
