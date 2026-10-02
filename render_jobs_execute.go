package main

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// The render encode step.
//
// Resolves an authorised source URL (capability proxy or local file), prepares
// subtitles, and invokes FFmpeg to produce the clip.

// executeRenderSpec encodes one render and returns a short excerpt of the
// burned-in subtitle dialogue, which the caller records so the download
// filename can identify the clip.
func (a *Application) executeRenderSpec(ctx context.Context, spec renderJobSpec, outputPartial string) (string, error) {
	callerScoped := false
	var callerAccess *PlexAccess
	if current := activeRenderCallerAccess(ctx); current != nil {
		callerScoped = true
		callerAccess = current
	}
	sourceURL := ""
	var capabilityRelease func()
	if callerScoped {
		var err error
		proxy, err := a.ensureMediaProxy()
		if err != nil {
			return "", newRenderStageFailure("source", "source_unavailable", errors.New("Plex capability proxy is unavailable"))
		}
		sourceURL, capabilityRelease, err = proxy.IssueWithTTL(ctx, callerAccess, spec.PartKey, renderTimeout)
		if err != nil {
			return "", newRenderStageFailure("source", "source_unavailable", err)
		}
		defer capabilityRelease()
	} else {
		sourceURL = fmt.Sprintf("%s%s?X-Plex-Token=%s", a.config.Plex.Host, spec.PartKey, a.config.Plex.Token)
	}
	// Prefer the local filesystem path over a Plex HTTP URL when available.
	// configureFFmpegHTTPRecovery is a no-op for local paths so no cleanup is needed.
	if spec.PartFile != "" {
		sourceURL = spec.PartFile
	}
	from := formatRenderTimestamp(spec.FromMs)
	to := formatRenderTimestamp(spec.ToMs)
	var subtitleFile string
	if spec.SubtitleIndex >= 0 && !spec.SubtitlePGS {
		if spec.SubtitleExternal {
			source := subtitleSource{
				StreamKey: spec.SubtitleStreamKey,
				Codec:     spec.SubtitleCodec,
				Format:    spec.SubtitleFormat,
				External:  true,
			}
			var err error
			if callerScoped {
				entries, downloadErr := a.downloadSubtitleWithAccess(ctx, source.StreamKey, subtitleSourceCodec(source), callerAccess, a.plexResources)
				if downloadErr == nil {
					subtitleFile, downloadErr = WriteClipSRT(entries, spec.FromMs, spec.ToMs, spec.SubtitleOffsetMs)
					if errors.Is(downloadErr, ErrNoUsableSubtitleCues) {
						subtitleFile, downloadErr = "", nil
					}
				}
				err = downloadErr
			} else {
				subtitleFile, err = a.prepareExternalSubtitleWithToken(ctx, source, spec.FromMs, spec.ToMs, []int64{spec.SubtitleOffsetMs}, a.config.Plex.Token)
			}
			if err != nil {
				if errors.Is(err, ErrNoUsableSubtitleCues) {
					subtitleFile = ""
				} else {
					return "", newRenderStageFailure("subtitle", "subtitle_unavailable", err)
				}
			}
		} else {
			embeddedIndex := spec.SubtitleEmbeddedIndex
			if embeddedIndex < 0 {
				embeddedIndex = spec.SubtitleIndex
			}
			if callerScoped && spec.PartFile == "" {
				proxy, proxyErr := a.ensureMediaProxy()
				if proxyErr != nil {
					return "", newRenderStageFailure("subtitle", "subtitle_unavailable", proxyErr)
				}
				subtitleURL, releaseCapability, issueErr := proxy.IssueWithTTL(ctx, callerAccess, spec.PartKey, renderTimeout)
				if issueErr != nil {
					return "", newRenderStageFailure("subtitle", "subtitle_unavailable", issueErr)
				}
				defer releaseCapability()
				releaseFFmpeg, acquireErr := a.acquireFFmpeg(ctx)
				if acquireErr != nil {
					return "", classifyRenderStageError("subtitle", acquireErr)
				}
				var subtitleErr error
				subtitleFile, subtitleErr = func() (string, error) {
					defer releaseFFmpeg()
					return extractSubtitleContextFn(ctx, subtitleURL, from, to, embeddedIndex, spec.SubtitleOffsetMs)
				}()
				if subtitleErr != nil {
					if errors.Is(subtitleErr, ErrNoUsableSubtitleCues) {
						subtitleFile = ""
					} else {
						return "", classifyRenderStageError("subtitle", subtitleErr)
					}
				}
				goto subtitleReady
			}
			release, err := a.acquireFFmpeg(ctx)
			if err != nil {
				return "", classifyRenderStageError("subtitle", err)
			}
			subtitleFile, err = func() (string, error) {
				defer release()
				return extractSubtitleContextFn(ctx, sourceURL, from, to, embeddedIndex, spec.SubtitleOffsetMs)
			}()
			if err != nil {
				if errors.Is(err, ErrNoUsableSubtitleCues) {
					subtitleFile = ""
				} else {
					return "", classifyRenderStageError("subtitle", err)
				}
			}
		}
	subtitleReady:
		defer os.Remove(subtitleFile)
	}
	params := FfmpegParams{
		URL: sourceURL, From: from, To: to, Filename: "job.mp4", OutputPath: outputPartial,
		Codec: a.config.Ffmpeg.Codec, Height: spec.Height, QP: spec.QP,
		AudioMode:    spec.AudioMode,
		SubtitleFile: subtitleFile, SubtitleIndex: -1, SubtitleOffsetMs: spec.SubtitleOffsetMs, Context: ctx,
		Metadata: FfmpegParamsMetadata{Title: spec.Title},
	}
	if spec.SubtitlePGS {
		params.SubtitleIndex = spec.SubtitleEmbeddedIndex
		if params.SubtitleIndex < 0 {
			params.SubtitleIndex = spec.SubtitleIndex
		}
	}
	release, err := a.acquireFFmpeg(ctx)
	if err != nil {
		return "", classifyRenderStageError("encoder", err)
	}
	// Capture the clip's dialogue before the temporary SRT is discarded, so the
	// download filename can identify the clip by what was said in it.
	var snippet string
	if subtitleFile != "" {
		if entries, parseErr := ParseSRT(subtitleFile); parseErr == nil {
			snippet = subtitleSnippetFromEntries(entries)
		}
	}
	_, err = doFfmpegFn(params)
	release()
	if err != nil {
		return "", classifyRenderError(err)
	}
	return snippet, nil
}
