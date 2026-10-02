package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/LukeHagar/plexgo/models/components"
	"github.com/gofiber/fiber/v3"
)

// HTTP handlers for render jobs.
//
// Creates, reports and serves render jobs, and renders structured API errors
// and timestamps shared by the other render-job handlers.

func (m *renderJobManager) defaultExecute(app *Application) renderJobExecutor {
	return func(ctx context.Context, spec renderJobSpec, outputPartial string) (string, error) {
		return app.executeRenderSpec(ctx, spec, outputPartial)
	}
}

func (a *API) createRenderJob(ctx fiber.Ctx) error {
	user := UserFromContext(ctx.UserContext())
	if user == nil || user.Uuid == "" {
		return renderAPIError(ctx, http.StatusUnauthorized, "authentication required")
	}
	contentType, _, contentTypeErr := mime.ParseMediaType(ctx.Get("Content-Type"))
	if contentTypeErr != nil || strings.ToLower(contentType) != "application/json" {
		return renderAPIError(ctx, http.StatusUnsupportedMediaType, "content type must be application/json")
	}
	body := ctx.Body()
	if len(body) > renderMaxBodyBytes {
		return renderAPIError(ctx, http.StatusRequestEntityTooLarge, "request body is too large")
	}
	request, err := decodeRenderJobRequest(body)
	if err != nil {
		return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", "invalid render job request")
	}
	if _, err := parseAudioMode(string(request.AudioMode)); err != nil {
		return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", "audioMode is invalid")
	}
	if err := validateRenderJobRequestFields(request); err != nil {
		return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", err.Error())
	}

	var sessions []sessionMetadata
	var selection previewSessionSelection
	userScoped := request.PartID != nil
	var metadataItem *components.Metadata
	var resolvedPart *components.Part
	if userScoped {
		metadataItem, err = a.app.getMetadataItem(ctx.UserContext(), request.RatingKey, true)
		if err == nil {
			media, part, sourceErr := resolveLibraryMetadataSource(metadataItem, request.MediaID, *request.PartID)
			if sourceErr != nil {
				err = sourceErr
			} else {
				resolvedPart = part
				selection = previewSessionSelection{mediaID: media.ID, partID: part.ID, duration: mediaDurationFromSource(media, part), selected: part.ID}
			}
		}
	} else {
		sessions, err = a.app.GetSessions(ctx.UserContext())
		if err != nil {
			log.Printf("render job session validation failed: %s", redactedDiagnostic(err))
			ctx.Set("Retry-After", "5")
			return renderAPIErrorCode(ctx, http.StatusServiceUnavailable, "session_unavailable", "could not validate the requested session")
		}
		selection, err = selectPreviewSessionSource(sessions, request.RatingKey, request.MediaID, true)
		if err != nil {
			return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", err.Error())
		}
		metadataItem, err = a.app.getMetadataItem(ctx.UserContext(), request.RatingKey, false)
	}
	if err != nil {
		log.Printf("render job library metadata validation failed: %s", redactedDiagnostic(err))
		if !userScoped {
			ctx.Set("Retry-After", "5")
			return renderAPIErrorCode(ctx, http.StatusServiceUnavailable, "metadata_unavailable", "could not validate the requested media")
		}
		return renderSourceAPIError(ctx, err)
	}
	if metadataItem == nil {
		return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", "requested media is unavailable")
	}
	spec, err := validateRenderJobRequestWithMetadataAndItem(request, *user, sessions, metadataItem.Media, selection, metadataItem)
	if err != nil {
		return renderAPIErrorCode(ctx, http.StatusUnprocessableEntity, "validation_error", err.Error())
	}
	// Populate the local filesystem path so the render worker can bypass Plex
	// HTTP streaming when the media file is directly accessible on disk.
	if resolvedPart != nil {
		spec.PartFile, _ = a.app.resolveLocalPartFile(resolvedPart)
	} else {
		// Session path: sessions often omit Part.File metadata. The library
		// metadata item is always fetched on this path and carries the full
		// Part.File, so resolve from there using the validated part ID.
		for i := range metadataItem.Media {
			for j := range metadataItem.Media[i].Part {
				if metadataItem.Media[i].Part[j].ID == spec.PartID {
					spec.PartFile, _ = a.app.resolveLocalPartFile(&metadataItem.Media[i].Part[j])
					break
				}
			}
		}
	}
	var callerAccess *PlexAccess
	if userScoped {
		callerAccess, _, err = a.app.callerPlexAccess(ctx.UserContext(), "")
		if err != nil {
			return renderAPIErrorCode(ctx, http.StatusServiceUnavailable, "source_unavailable", "caller Plex source is unavailable")
		}
		spec.CallerScoped = true
	}
	job, err := a.app.renderJobs.enqueueWithCallerLease(user.Uuid, spec, callerAccess)
	if err != nil {
		if errors.Is(err, errRenderQueueFull) || errors.Is(err, errRenderOwnerLimit) {
			ctx.Set("Retry-After", "5")
			return renderAPIError(ctx, http.StatusTooManyRequests, "render queue is busy")
		}
		ctx.Set("Retry-After", "5")
		return renderAPIError(ctx, http.StatusServiceUnavailable, "render storage is unavailable")
	}
	result, _ := a.app.renderJobs.status(job.id, user.Uuid)
	ctx.Status(http.StatusAccepted)
	ctx.Set("Cache-Control", "no-store")
	ctx.Set("Referrer-Policy", "no-referrer")
	ctx.Set("Location", "/render-jobs/"+job.id)
	return ctx.JSON(result)
}

func decodeRenderJobRequest(body []byte) (RenderJobCreateRequest, error) {
	request := RenderJobCreateRequest{SubtitleIndex: -1}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return RenderJobCreateRequest{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return RenderJobCreateRequest{}, errors.New("trailing JSON")
	}
	return request, nil
}

func (a *API) getRenderJob(ctx fiber.Ctx) error {
	user := UserFromContext(ctx.UserContext())
	if user == nil || user.Uuid == "" {
		return renderAPIError(ctx, http.StatusUnauthorized, "authentication required")
	}
	ctx.Set("Cache-Control", "no-store")
	ctx.Set("Referrer-Policy", "no-referrer")
	result, err := a.app.renderJobs.status(ctx.Params("id"), user.Uuid)
	if err != nil {
		return renderAPIError(ctx, http.StatusNotFound, "render job not found")
	}
	if result.Status == renderExpired {
		return renderAPIErrorCode(ctx, http.StatusGone, "render_expired", "render job has expired")
	}
	return ctx.JSON(result)
}

func (a *API) downloadRenderJob(ctx fiber.Ctx) error {
	user := UserFromContext(ctx.UserContext())
	if user == nil || user.Uuid == "" {
		return renderAPIError(ctx, http.StatusUnauthorized, "authentication required")
	}
	path, spec, release, err := a.app.renderJobs.acquireDownloadWithSpec(ctx.Params("id"), user.Uuid)
	if err != nil {
		switch {
		case errors.Is(err, errRenderNotFound):
			return renderAPIError(ctx, http.StatusNotFound, "render job not found")
		case errors.Is(err, errRenderExpired):
			return renderAPIError(ctx, http.StatusGone, "render job has expired")
		default:
			return renderAPIError(ctx, http.StatusConflict, "render job is not complete")
		}
	}
	file, err := os.Open(path)
	if err != nil {
		release()
		return renderAPIErrorCode(ctx, http.StatusGone, "render_expired", "render job has expired")
	}
	info, err := file.Stat()
	if err != nil {
		release()
		return renderAPIErrorCode(ctx, http.StatusGone, "render_expired", "render job has expired")
	}
	ctx.Type(".mp4")
	ctx.Set("Cache-Control", "no-store")
	ctx.Set("Referrer-Policy", "no-referrer")
	ctx.Set(fiber.HeaderContentDisposition, renderDownloadContentDisposition(spec))
	ctx.Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	ctx.Response().SetBodyStreamWriter(func(writer *bufio.Writer) {
		defer file.Close()
		defer release()
		if _, err := io.Copy(writer, file); err != nil {
			log.Printf("render download stream failed: %s", redactedDiagnostic(err))
		}
	})
	return nil
}

func renderAPIError(ctx fiber.Ctx, status int, message string) error {
	return renderAPIErrorCode(ctx, status, "request_error", message)
}

func renderAPIErrorCode(ctx fiber.Ctx, status int, code, message string) error {
	ctx.Status(status)
	ctx.Set("Cache-Control", "no-store")
	return ctx.JSON(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func formatRenderTimestamp(ms int64) string {
	hours := ms / 3600000
	ms %= 3600000
	minutes := ms / 60000
	ms %= 60000
	seconds := ms / 1000
	ms %= 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, seconds, ms)
}
