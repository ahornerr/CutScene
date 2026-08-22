package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	defaultYouTubeExecutable = "yt-dlp"
	defaultYouTubeTimeout    = 15 * time.Minute
	defaultYouTubeRetention  = time.Hour
	maxYouTubeURLBytes       = 2048
)

type youtubeVideo struct {
	ID  string
	URL string
}

// validateYouTubeURL deliberately accepts only URLs which identify one video.
// yt-dlp still receives --no-playlist as a second, independent boundary.
func validateYouTubeURL(raw string) (youtubeVideo, error) {
	if len(raw) == 0 || len(raw) > maxYouTubeURLBytes {
		return youtubeVideo{}, errors.New("YouTube URL is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.User != nil || parsed.Host == "" || parsed.Port() != "" {
		return youtubeVideo{}, errors.New("YouTube URL must use HTTPS")
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "youtube.com", "www.youtube.com", "m.youtube.com", "youtu.be":
	default:
		return youtubeVideo{}, errors.New("URL host is not an allowed YouTube host")
	}
	if parsed.Fragment != "" {
		return youtubeVideo{}, errors.New("YouTube URL fragments are not allowed")
	}
	query := parsed.Query()
	for key := range query {
		if strings.EqualFold(key, "list") {
			return youtubeVideo{}, errors.New("playlist URLs are not allowed")
		}
	}

	videoID := ""
	if host == "youtu.be" {
		trimmedPath := strings.Trim(parsed.Path, "/")
		if trimmedPath == "" || strings.Contains(trimmedPath, "/") {
			return youtubeVideo{}, errors.New("YouTube URL must identify one video")
		}
		videoID = trimmedPath
	} else {
		switch {
		case parsed.Path == "/watch":
			videoID = query.Get("v")
		case strings.HasPrefix(parsed.Path, "/shorts/"), strings.HasPrefix(parsed.Path, "/embed/"), strings.HasPrefix(parsed.Path, "/live/"):
			parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			if len(parts) == 2 {
				videoID = parts[1]
			}
		}
	}
	if !validYouTubeVideoID(videoID) {
		return youtubeVideo{}, errors.New("YouTube URL must identify one video")
	}
	return youtubeVideo{ID: videoID, URL: "https://www.youtube.com/watch?v=" + videoID}, nil
}

func validYouTubeVideoID(value string) bool {
	if len(value) < 6 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

type youtubeSourceResponse struct {
	SourceID   string               `json:"sourceId"`
	URL        string               `json:"url"`
	Title      string               `json:"title"`
	Duration   int64                `json:"duration"`
	Thumb      string               `json:"thumb,omitempty"`
	Artwork    string               `json:"artwork,omitempty"`
	SourceType string               `json:"_sourceType"`
	RatingKey  string               `json:"ratingKey"`
	Type       string               `json:"type"`
	MediaID    int64                `json:"mediaId"`
	PartID     int64                `json:"partId"`
	Media      []youtubeSourceMedia `json:"Media"`
}

type youtubeSourceMedia struct {
	ID       int64               `json:"id"`
	Duration int64               `json:"duration"`
	Part     []youtubeSourcePart `json:"Part"`
}

type youtubeSourcePart struct {
	ID       int64 `json:"id"`
	Duration int64 `json:"duration"`
}

type youtubeSourceRecord struct {
	Response  youtubeSourceResponse
	OwnerUUID string
	VideoID   string
	Path      string
	TempDir   string
	ExpiresAt time.Time
}

type youtubeSourceRegistry struct {
	mu        sync.Mutex
	sources   map[string]youtubeSourceRecord
	retention time.Duration
	now       func() time.Time
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newYouTubeSourceRegistry(parent context.Context, retention time.Duration) *youtubeSourceRegistry {
	if parent == nil {
		parent = context.Background()
	}
	if retention <= 0 {
		retention = defaultYouTubeRetention
	}
	r := &youtubeSourceRegistry{
		sources: make(map[string]youtubeSourceRecord), retention: retention,
		now: time.Now, stop: make(chan struct{}), done: make(chan struct{}),
	}
	go r.cleanupLoop(parent)
	return r
}

func (r *youtubeSourceRegistry) cleanupLoop(parent context.Context) {
	interval := r.retention / 2
	if interval < time.Second {
		interval = time.Second
	}
	if interval > time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(r.done)
	for {
		select {
		case <-ticker.C:
			r.cleanupExpired()
		case <-r.stop:
			return
		case <-parent.Done():
			r.cleanupExpired()
			return
		}
	}
}

func (r *youtubeSourceRegistry) cleanupExpired() {
	if r == nil {
		return
	}
	now := r.now()
	var expired []youtubeSourceRecord
	r.mu.Lock()
	for id, source := range r.sources {
		if !now.Before(source.ExpiresAt) {
			delete(r.sources, id)
			expired = append(expired, source)
		}
	}
	r.mu.Unlock()
	for _, source := range expired {
		_ = os.RemoveAll(source.TempDir)
	}
}

func (r *youtubeSourceRegistry) put(source youtubeSourceRecord) {
	r.cleanupExpired()
	source.OwnerUUID = normalizeOwnerIdentity(source.OwnerUUID)
	r.mu.Lock()
	r.sources[source.Response.SourceID] = source
	r.mu.Unlock()
}

func (r *youtubeSourceRegistry) get(sourceID, ownerUUID string) (youtubeSourceRecord, bool) {
	r.cleanupExpired()
	ownerUUID = normalizeOwnerIdentity(ownerUUID)
	r.mu.Lock()
	source, ok := r.sources[sourceID]
	if ok && source.OwnerUUID != ownerUUID {
		r.mu.Unlock()
		return youtubeSourceRecord{}, false
	}
	if ok && !r.now().Before(source.ExpiresAt) {
		delete(r.sources, sourceID)
		ok = false
	}
	r.mu.Unlock()
	if !ok {
		if source.TempDir != "" {
			_ = os.RemoveAll(source.TempDir)
		}
		return youtubeSourceRecord{}, false
	}
	return source, true
}

func (r *youtubeSourceRegistry) close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		close(r.stop)
		<-r.done
		r.mu.Lock()
		sources := make([]youtubeSourceRecord, 0, len(r.sources))
		for id, source := range r.sources {
			delete(r.sources, id)
			sources = append(sources, source)
		}
		r.mu.Unlock()
		for _, source := range sources {
			_ = os.RemoveAll(source.TempDir)
		}
	})
}

type youtubeMetadata struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Duration  float64 `json:"duration"`
	Thumbnail string  `json:"thumbnail"`
}

type youtubeCommandRunner func(context.Context, string, []string, string) ([]byte, error)

func runYouTubeCommand(ctx context.Context, executable string, args []string, dir string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = dir
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("yt-dlp failed: %w", err)
	}
	return output, nil
}

func youtubeProbeArgs(sourceURL string) []string {
	return []string{"--ignore-config", "--no-playlist", "--no-warnings", "--dump-single-json", "--no-download", sourceURL}
}

func youtubeDownloadArgs(sourceURL, outputDir string) []string {
	return []string{"--ignore-config", "--no-playlist", "--no-warnings", "--format", "bv*+ba/b", "--output", filepath.Join(outputDir, "%(id)s.%(ext)s"), "--print", "after_move:filepath", sourceURL}
}

func youtubeCompatID(videoID string) int64 {
	digest := sha256.Sum256([]byte(videoID))
	value := int64(0)
	for _, b := range digest[:8] {
		value = (value << 8) | int64(b)
	}
	value &= 0x7fffffffffffffff
	if value == 0 {
		value = 1
	}
	return value
}

func (a *Application) youtubeExecutable() string {
	if value := strings.TrimSpace(a.config.YouTube.Executable); value != "" {
		return value
	}
	return defaultYouTubeExecutable
}

func (a *Application) youtubeTimeout() time.Duration {
	if a.config.YouTube.Timeout > 0 {
		return a.config.YouTube.Timeout
	}
	return defaultYouTubeTimeout
}

func (a *Application) ensureYouTubeSources() *youtubeSourceRegistry {
	if a.youtubeSources == nil {
		parent := a.lifetime
		if parent == nil {
			parent = context.Background()
		}
		a.youtubeSources = newYouTubeSourceRegistry(parent, a.config.YouTube.Retention)
	}
	return a.youtubeSources
}

func (a *Application) ingestYouTubeSource(ctx context.Context, ownerUUID, rawURL string) (youtubeSourceResponse, error) {
	video, err := validateYouTubeURL(rawURL)
	if err != nil {
		return youtubeSourceResponse{}, &sourceValidationError{message: err.Error()}
	}
	if strings.TrimSpace(ownerUUID) == "" {
		return youtubeSourceResponse{}, errors.New("authenticated user is missing a stable id")
	}
	workDir, err := os.MkdirTemp("", "cutscene-youtube-"+video.ID+"-")
	if err != nil {
		return youtubeSourceResponse{}, err
	}
	runner := a.youtubeRunner
	if runner == nil {
		runner = runYouTubeCommand
	}
	operationCtx, cancel := context.WithTimeout(ctx, a.youtubeTimeout())
	defer cancel()
	probeOutput, err := runner(operationCtx, a.youtubeExecutable(), youtubeProbeArgs(video.URL), workDir)
	if err != nil {
		_ = os.RemoveAll(workDir)
		return youtubeSourceResponse{}, err
	}
	var metadata youtubeMetadata
	if err := json.Unmarshal(probeOutput, &metadata); err != nil || metadata.ID != video.ID || strings.TrimSpace(metadata.Title) == "" {
		_ = os.RemoveAll(workDir)
		return youtubeSourceResponse{}, errors.New("yt-dlp returned invalid YouTube metadata")
	}
	downloadOutput, err := runner(operationCtx, a.youtubeExecutable(), youtubeDownloadArgs(video.URL, workDir), workDir)
	if err != nil {
		_ = os.RemoveAll(workDir)
		return youtubeSourceResponse{}, err
	}
	path, err := youtubeDownloadedPath(downloadOutput, workDir)
	if err != nil {
		_ = os.RemoveAll(workDir)
		return youtubeSourceResponse{}, err
	}
	duration := int64(metadata.Duration * 1000)
	if duration < 0 {
		duration = 0
	}
	mediaID := youtubeCompatID(video.ID)
	partID := mediaID
	response := youtubeSourceResponse{
		SourceID: uuid.NewString(), URL: video.URL, Title: strings.TrimSpace(metadata.Title),
		Duration: duration, Thumb: metadata.Thumbnail, Artwork: metadata.Thumbnail, SourceType: "youtube",
		RatingKey: "youtube:" + video.ID, Type: "movie", MediaID: mediaID, PartID: partID,
		Media: []youtubeSourceMedia{{ID: mediaID, Duration: duration, Part: []youtubeSourcePart{{ID: partID, Duration: duration}}}},
	}
	retention := a.config.YouTube.Retention
	if retention <= 0 {
		retention = defaultYouTubeRetention
	}
	registry := a.ensureYouTubeSources()
	registry.put(youtubeSourceRecord{
		Response: response, OwnerUUID: ownerUUID, VideoID: video.ID, Path: path,
		TempDir: workDir, ExpiresAt: registry.now().Add(retention),
	})
	return response, nil
}

func youtubeDownloadedPath(output []byte, workDir string) (string, error) {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 0 {
		return "", errors.New("yt-dlp did not report a downloaded media path")
	}
	reported := strings.TrimSpace(lines[len(lines)-1])
	if reported == "" || filepath.IsAbs(reported) && !pathWithinDir(reported, workDir) {
		return "", errors.New("yt-dlp reported an invalid media path")
	}
	path := reported
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}
	path, err := filepath.Abs(path)
	if err != nil || !pathWithinDir(path, workDir) {
		return "", errors.New("yt-dlp reported an invalid media path")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", errors.New("downloaded media is unavailable")
	}
	return path, nil
}

func pathWithinDir(path, dir string) bool {
	path, _ = filepath.Abs(path)
	dir, _ = filepath.Abs(dir)
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ""
}
