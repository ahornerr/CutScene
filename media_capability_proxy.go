package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	mediaCapabilityTTL    = 2 * time.Minute
	mediaCapabilityMaxTTL = renderTimeout
)

type plexMediaCapability struct {
	access  *PlexAccess
	path    string
	expires time.Time
	ctx     context.Context
	cancel  context.CancelFunc
	release sync.Once
}

type plexCapabilityProxy struct {
	resolver *PlexResourceResolver
	server   *http.Server
	listener net.Listener
	baseURL  string
	ttl      time.Duration
	now      func() time.Time

	mu    sync.Mutex
	items map[string]*plexMediaCapability
}

type mediaByteRange struct {
	start, end int64
	hasEnd     bool
}

type mediaContentRange struct {
	start, end, total int64
	unsatisfied       bool
}

func parseSingleMediaRange(value string) (*mediaByteRange, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return nil, errors.New("invalid media range")
	}
	part := strings.TrimSpace(strings.TrimPrefix(value, "bytes="))
	pieces := strings.Split(part, "-")
	if len(pieces) != 2 || pieces[0] == "" {
		return nil, errors.New("invalid media range")
	}
	start, startErr := strconv.ParseInt(pieces[0], 10, 64)
	if startErr != nil || start < 0 {
		return nil, errors.New("invalid media range")
	}
	if pieces[1] == "" {
		return &mediaByteRange{start: start}, nil
	}
	end, endErr := strconv.ParseInt(pieces[1], 10, 64)
	if endErr != nil || end < start {
		return nil, errors.New("invalid media range")
	}
	return &mediaByteRange{start: start, end: end, hasEnd: true}, nil
}

func parseMediaContentRange(value string) (mediaContentRange, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "bytes ") {
		return mediaContentRange{}, errors.New("invalid content range")
	}
	parts := strings.SplitN(strings.TrimPrefix(value, "bytes "), "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return mediaContentRange{}, errors.New("invalid content range")
	}
	if parts[0] == "*" {
		total, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || total < 0 {
			return mediaContentRange{}, errors.New("invalid content range")
		}
		return mediaContentRange{total: total, unsatisfied: true}, nil
	}
	span := strings.Split(parts[0], "-")
	if len(span) != 2 {
		return mediaContentRange{}, errors.New("invalid content range")
	}
	start, startErr := strconv.ParseInt(span[0], 10, 64)
	end, endErr := strconv.ParseInt(span[1], 10, 64)
	total, totalErr := strconv.ParseInt(parts[1], 10, 64)
	if startErr != nil || endErr != nil || totalErr != nil || start < 0 || end < start || total <= end {
		return mediaContentRange{}, errors.New("invalid content range")
	}
	return mediaContentRange{start: start, end: end, total: total}, nil
}

func parseMediaContentLength(header string) (int64, error) {
	if strings.TrimSpace(header) == "" {
		return 0, errors.New("missing content length")
	}
	value, err := strconv.ParseInt(strings.TrimSpace(header), 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("invalid content length")
	}
	return value, nil
}

func validateMediaUpstreamResponse(response *http.Response, requested *mediaByteRange) (int64, error) {
	if response == nil {
		return 0, errors.New("missing media response")
	}
	if encoding := strings.TrimSpace(response.Header.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
		return 0, errors.New("encoded media response is not supported")
	}
	if requested == nil {
		if response.StatusCode != http.StatusOK {
			return 0, errors.New("unexpected media response status")
		}
		return parseMediaContentLength(response.Header.Get("Content-Length"))
	}
	if response.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		contentRange, err := parseMediaContentRange(response.Header.Get("Content-Range"))
		if err != nil || !contentRange.unsatisfied || requested.start < contentRange.total {
			return 0, errors.New("invalid unsatisfied media range")
		}
		return -1, nil
	}
	if response.StatusCode != http.StatusPartialContent {
		return 0, errors.New("upstream ignored media range")
	}
	contentRange, err := parseMediaContentRange(response.Header.Get("Content-Range"))
	if err != nil || contentRange.unsatisfied || contentRange.start != requested.start || requested.start >= contentRange.total {
		return 0, errors.New("inconsistent media content range")
	}
	expectedEnd := contentRange.total - 1
	if requested.hasEnd && requested.end < expectedEnd {
		expectedEnd = requested.end
	}
	if contentRange.end != expectedEnd {
		return 0, errors.New("inconsistent media content range")
	}
	length, err := parseMediaContentLength(response.Header.Get("Content-Length"))
	if err != nil || length != contentRange.end-contentRange.start+1 {
		return 0, errors.New("inconsistent media content length")
	}
	return length, nil
}

func abortMediaDownstream(response http.ResponseWriter) {
	hijacker, ok := response.(http.Hijacker)
	if !ok {
		return
	}
	connection, _, err := hijacker.Hijack()
	if err == nil {
		_ = connection.Close()
	}
}

func newPlexCapabilityProxy(resolver *PlexResourceResolver) (*plexCapabilityProxy, error) {
	if resolver == nil {
		return nil, errors.New("Plex resolver is unavailable")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("could not bind Plex capability proxy")
	}
	proxy := &plexCapabilityProxy{
		resolver: resolver,
		listener: listener,
		baseURL:  "http://" + listener.Addr().String(),
		ttl:      mediaCapabilityTTL,
		now:      time.Now,
		items:    make(map[string]*plexMediaCapability),
	}
	proxy.server = &http.Server{Handler: http.HandlerFunc(proxy.serveHTTP)}
	go func() { _ = proxy.server.Serve(listener) }()
	return proxy, nil
}

func (p *plexCapabilityProxy) URL() string {
	if p == nil {
		return ""
	}
	return p.baseURL
}

func (p *plexCapabilityProxy) Issue(ctx context.Context, access *PlexAccess, rootRelativePath string) (string, func(), error) {
	return p.IssueWithTTL(ctx, access, rootRelativePath, p.ttl)
}

// IssueWithTTL creates an operation-scoped capability. The requested TTL is
// bounded, and a parent deadline always wins, so capabilities cannot outlive
// their operation or become an unbounded credential-bearing handle.
func (p *plexCapabilityProxy) IssueWithTTL(ctx context.Context, access *PlexAccess, rootRelativePath string, requestedTTL time.Duration) (string, func(), error) {
	if p == nil || access == nil {
		return "", nil, errors.New("Plex capability proxy is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if requestedTTL <= 0 {
		return "", nil, errors.New("Plex capability TTL is invalid")
	}
	if requestedTTL > mediaCapabilityMaxTTL {
		requestedTTL = mediaCapabilityMaxTTL
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", nil, ctx.Err()
		}
		if remaining < requestedTTL {
			requestedTTL = remaining
		}
	}
	_, err := newPlexRequest(ctx, access, http.MethodGet, rootRelativePath)
	if err != nil {
		return "", nil, err
	}
	id, err := randomCapabilityID()
	if err != nil {
		return "", nil, err
	}
	leaseContext, cancel := context.WithTimeout(ctx, requestedTTL)
	lease := &plexMediaCapability{
		access:  access,
		path:    rootRelativePath,
		expires: p.now().Add(requestedTTL),
		ctx:     leaseContext,
		cancel:  cancel,
	}
	p.removeExpired(p.now())
	p.mu.Lock()
	p.items[id] = lease
	p.mu.Unlock()
	context.AfterFunc(leaseContext, func() { p.revoke(id, lease) })
	release := func() { p.revoke(id, lease) }
	return p.baseURL + "/plex-media/" + id, release, nil
}

func randomCapabilityID() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func (p *plexCapabilityProxy) revoke(id string, lease *plexMediaCapability) {
	if lease == nil {
		return
	}
	lease.release.Do(func() {
		lease.cancel()
		p.mu.Lock()
		if current, ok := p.items[id]; ok && current == lease {
			delete(p.items, id)
		}
		p.mu.Unlock()
	})
}

func (p *plexCapabilityProxy) removeExpired(now time.Time) {
	p.mu.Lock()
	expired := make([]struct {
		id    string
		lease *plexMediaCapability
	}, 0)
	for id, lease := range p.items {
		if !now.Before(lease.expires) {
			expired = append(expired, struct {
				id    string
				lease *plexMediaCapability
			}{id: id, lease: lease})
		}
	}
	p.mu.Unlock()
	for _, item := range expired {
		p.revoke(item.id, item.lease)
	}
}

func (p *plexCapabilityProxy) serveHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "plex-media" || parts[1] == "" || request.URL.RawQuery != "" {
		response.WriteHeader(http.StatusNotFound)
		return
	}
	p.removeExpired(p.now())
	p.mu.Lock()
	lease := p.items[parts[1]]
	p.mu.Unlock()
	if lease == nil || lease.ctx.Err() != nil {
		response.WriteHeader(http.StatusNotFound)
		return
	}
	requestContext, cancelRequest := context.WithCancel(lease.ctx)
	stopInbound := context.AfterFunc(request.Context(), cancelRequest)
	defer func() {
		stopInbound()
		cancelRequest()
	}()
	requestedRange, err := parseSingleMediaRange(request.Header.Get("Range"))
	if err != nil {
		response.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	upstream, err := newPlexRequest(requestContext, lease.access, request.Method, lease.path)
	if err != nil {
		response.WriteHeader(http.StatusNotFound)
		return
	}
	if requestedRange != nil {
		upstream.Header.Set("Range", request.Header.Get("Range"))
	}
	upstream.Header.Set("Accept", request.Header.Get("Accept"))
	upstream.Header.Set("Accept-Encoding", "identity")
	upstreamResponse, err := p.resolver.DoPlexMediaRequest(requestContext, lease.access, upstream)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			response.WriteHeader(http.StatusRequestTimeout)
		} else {
			response.WriteHeader(http.StatusBadGateway)
		}
		return
	}
	defer upstreamResponse.Body.Close()
	expectedLength, err := validateMediaUpstreamResponse(upstreamResponse, requestedRange)
	if err != nil {
		response.WriteHeader(http.StatusBadGateway)
		return
	}
	if upstreamResponse.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		response.Header().Set("Content-Range", upstreamResponse.Header.Get("Content-Range"))
		response.Header().Set("Content-Length", "0")
		response.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	for _, header := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if value := upstreamResponse.Header.Get(header); value != "" {
			response.Header().Set(header, value)
		}
	}
	if expectedLength >= 0 {
		response.Header().Set("Content-Length", strconv.FormatInt(expectedLength, 10))
	}
	response.WriteHeader(upstreamResponse.StatusCode)
	if request.Method != http.MethodHead && expectedLength >= 0 {
		copied, copyErr := io.Copy(response, upstreamResponse.Body)
		if copyErr != nil || copied != expectedLength {
			abortMediaDownstream(response)
		}
	}
}

func (p *plexCapabilityProxy) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	items := make([]struct {
		id    string
		lease *plexMediaCapability
	}, 0, len(p.items))
	for id, lease := range p.items {
		items = append(items, struct {
			id    string
			lease *plexMediaCapability
		}{id: id, lease: lease})
	}
	p.mu.Unlock()
	for _, item := range items {
		p.revoke(item.id, item.lease)
	}
	if p.server != nil {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := p.server.Shutdown(shutdownContext)
		cancel()
		if errors.Is(err, context.DeadlineExceeded) {
			_ = p.server.Close()
		}
		return err
	}
	return nil
}
