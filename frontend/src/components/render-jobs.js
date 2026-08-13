import {millisToDuration} from "../utils";
import {clampSubtitleOffsetMs, formatSubtitleOffsetMs} from "./SubtitleOffsetControl";

// Render job states matching the backend renderJobState constants.
export const JOB_STATES = {
  QUEUED: 'queued',
  RUNNING: 'running',
  SUCCEEDED: 'succeeded',
  FAILED: 'failed',
  EXPIRED: 'expired',
}

// Audio mode enum values sent to the backend in preview URLs and render-job
// request bodies. The backend interprets these as FFmpeg audio filter
// presets. `standard` is the default passthrough.
export const AUDIO_MODES = {
  STANDARD: 'standard',
  DIALOGUE: 'dialogue',
  DIALOGUE_NORMALIZED: 'dialogue_normalized',
}

export const RENDER_RESOLUTIONS = {
  NATIVE: 'native',
  P1080: '1080p',
  P720: '720p',
  P480: '480p',
}

// Quality tiers are ordered from highest to lowest. Native is the safe
// fallback when the source is below the lowest tier or its dimensions are
// unavailable; it does not request an upscale.
export const RESOLUTION_CHOICES = [
  {value: RENDER_RESOLUTIONS.NATIVE, label: 'Extra high', detail: '4K', targetHeight: 2160},
  {value: RENDER_RESOLUTIONS.P1080, label: 'High', detail: '1080', targetHeight: 1080},
  {value: RENDER_RESOLUTIONS.P720, label: 'Medium', detail: '720', targetHeight: 720},
  {value: RENDER_RESOLUTIONS.P480, label: 'Low', detail: '480', targetHeight: 480},
]

export function getSourceMediaHeight(session) {
  const height = Number(session?.Media?.[0]?.height)
  return Number.isFinite(height) && height > 0 ? height : null
}

export function getAvailableResolutionChoices(sourceHeight) {
  const height = Number(sourceHeight)
  if (!Number.isFinite(height) || height <= 0 || height < 480) return [RESOLUTION_CHOICES[0]]
  return RESOLUTION_CHOICES.filter(choice => (
    choice.value === RENDER_RESOLUTIONS.NATIVE
      ? height >= choice.targetHeight
      : choice.targetHeight <= height
  ))
}

export function getSafeResolution(sourceHeight) {
  const choices = getAvailableResolutionChoices(sourceHeight)
  return choices[0].value
}

// User-facing labels and descriptions for each audio mode.
export const AUDIO_MODE_CHOICES = [
  {
    value: AUDIO_MODES.STANDARD,
    label: 'Standard stereo',
    description: 'Original audio track as encoded.',
  },
  {
    value: AUDIO_MODES.DIALOGUE,
    label: 'Dialogue boost',
    description: 'Centred speech is amplified for clearer voices.',
  },
  {
    value: AUDIO_MODES.DIALOGUE_NORMALIZED,
    label: 'Dialogue boost + normalize',
    description: 'Centred speech amplified and loudness normalized.',
  },
]

export function audioModeLabel(value) {
  return AUDIO_MODE_CHOICES.find(c => c.value === value)?.label || 'Standard stereo'
}

// Build the POST /render-jobs request body from the current clip spec.
// `subtitleOffsetMs` shifts subtitle timing in the rendered output — positive
// delays subtitles (later), negative advances them (earlier). It is always
// present in the payload so the backend can rely on the field.
//
// `partId` is included only for explicit library sources (session._sourceType
// === 'library'). Active-session sources omit it entirely so the backend
// follows the legacy session-resolution path. A library source with an invalid
// partId throws here rather than silently omitting the field — the backend
// requires partId to resolve an explicit library source, so a silent omission
// would route the request through the wrong (session-based) path.
export function buildRenderJobRequest(session, startPosition, endPosition, selectedSubtitle, audioMode, subtitleOffsetMs, resolution) {
  const mediaId = normalizeMediaId(session?.Media?.[0]?.Part?.[0]?.id)
  if (mediaId == null) throw new Error('The selected media part has an invalid media ID.')
  const body = {
    ratingKey: session.ratingKey,
    mediaId,
    fromMs: startPosition,
    toMs: endPosition,
    subtitleIndex: selectedSubtitle,
    audioMode: audioMode || AUDIO_MODES.STANDARD,
    subtitleOffsetMs: clampSubtitleOffsetMs(subtitleOffsetMs),
    resolution: resolution || RENDER_RESOLUTIONS.NATIVE,
  }
  if (session?._sourceType === 'library') {
    const partId = normalizePartId(session._partId)
    if (partId == null) throw new Error('The selected library source has an invalid part ID.')
    body.partId = partId
  }
  return body
}

// Build a request body from an immutable submitted spec (for re-render).
export function buildRenderJobRequestFromSpec(spec) {
  const mediaId = normalizeMediaId(spec?.mediaId)
  if (mediaId == null) throw new Error('The submitted render job has an invalid media ID.')
  const body = {
    ratingKey: spec.ratingKey,
    mediaId,
    fromMs: spec.fromMs,
    toMs: spec.toMs,
    subtitleIndex: spec.subtitleIndex,
    audioMode: spec.audioMode || AUDIO_MODES.STANDARD,
    subtitleOffsetMs: clampSubtitleOffsetMs(spec?.subtitleOffsetMs),
    resolution: spec?.resolution || RENDER_RESOLUTIONS.NATIVE,
  }
  // A library-source spec carries its partId; re-render must preserve it.
  // Reject (rather than silently omit) if the spec claims to be a library
  // source but lacks a valid partId — the backend needs it to resolve the
  // source without an active session.
  if (spec?.sourceType === 'library' || spec?.partId != null) {
    const partId = normalizePartId(spec?.partId)
    if (partId == null) throw new Error('The submitted render job has an invalid part ID.')
    body.partId = partId
  }
  return body
}

// Plex may return Part ids as strings. Keep the API payload numeric and reject
// malformed ids before a request reaches the backend's int64 decoder.
export function normalizeMediaId(value) {
  if (value == null || (typeof value === 'string' && value.trim() === '')) return null
  const numeric = Number(value)
  return Number.isFinite(numeric) && Number.isInteger(numeric) ? numeric : null
}

// Like normalizeMediaId, but for the explicit library-source part id. Returns
// null for absent/invalid values so callers can omit the field entirely.
export function normalizePartId(value) {
  if (value == null || value === '') return null
  const numeric = Number(value)
  return Number.isFinite(numeric) && Number.isInteger(numeric) && numeric > 0 ? numeric : null
}

// Library sources tag the selected session with `_sourceType: 'library'` and a
// numeric `_partId`. Active sessions carry neither field, so they resolve to
// null and the legacy request shape (no partId) is preserved. For library
// sources, an invalid partId returns null — callers that must include partId
// (buildRenderJobRequest) throw instead of silently omitting it.
export function getSourcePartId(session) {
  if (!session || session._sourceType !== 'library') return null
  return normalizePartId(session._partId)
}

// Validate a backend LibrarySearchResult before accepting it as a clip source.
// Returns a descriptive error string when the result is malformed, or null when
// it is acceptable. The workspace cannot function without a usable ratingKey,
// mediaId, and partId — rejecting up front prevents a broken workspace that
// can't fetch streams, preview, or render.
export function validateLibraryResult(result) {
  if (!result || typeof result !== 'object') return 'This library result is malformed.'
  const ratingKey = result.ratingKey
  if (typeof ratingKey !== 'string' || ratingKey.trim() === '') return 'This library result is missing a rating key.'
  const mediaId = normalizeMediaId(result.mediaId)
  if (mediaId == null || mediaId <= 0) return 'This library result has an invalid media ID.'
  const partId = normalizePartId(result.partId)
  if (partId == null) return 'This library result has an invalid part ID.'
  return null
}

// Whether an error code is a transport/polling issue (not a render failure).
export function isTransportError(error) {
  if (!error) return false
  return ['network_error', 'service_unavailable', 'auth_error', 'request_error'].includes(error.code)
}

// Snapshot an immutable submitted spec for display. This is captured at job
// creation time and never mutated — it represents what was actually submitted,
// not the current (possibly edited) controls.
export function snapshotJobSpec(session, startPosition, endPosition, selectedSubtitle, streams, audioMode, subtitleOffsetMs, resolution) {
  const stream = selectedSubtitle >= 0 ? streams.find(s => s.index === selectedSubtitle) : null
  const offsetMs = clampSubtitleOffsetMs(subtitleOffsetMs)
  return {
    ratingKey: session.ratingKey,
    mediaId: normalizeMediaId(session?.Media?.[0]?.Part?.[0]?.id),
    partId: getSourcePartId(session),
    sourceType: session?._sourceType === 'library' ? 'library' : 'session',
    title: session.title || session.grandparentTitle || '',
    fromMs: startPosition,
    toMs: endPosition,
    subtitleIndex: selectedSubtitle,
    subtitleLabel: stream
      ? (stream.language
          ? `${stream.language} (${stream.displayTitle || 'Track ' + (stream.index + 1)})`
          : stream.displayTitle || 'Track ' + (stream.index + 1))
      : 'None',
    clipDuration: Math.max(0, endPosition - startPosition),
    audioMode: audioMode || AUDIO_MODES.STANDARD,
    subtitleOffsetMs: offsetMs,
    subtitleOffsetLabel: `Offset ${formatSubtitleOffsetMs(offsetMs)}`,
    resolution: resolution || RENDER_RESOLUTIONS.NATIVE,
  }
}

// Human-readable labels for each terminal/active state.
export function jobStatusLabel(status) {
  switch (status) {
    case JOB_STATES.QUEUED: return 'Queued'
    case JOB_STATES.RUNNING: return 'Rendering'
    case JOB_STATES.SUCCEEDED: return 'Ready'
    case JOB_STATES.FAILED: return 'Failed'
    case JOB_STATES.EXPIRED: return 'Expired'
    default: return status || ''
  }
}

// Map backend error codes to understandable messages.
export function jobErrorMessage(error) {
  if (!error) return 'Rendering failed.'
  return error.message || 'Rendering failed.'
}

// Whether a state is terminal (no further polling).
export function isTerminal(status) {
  return status === JOB_STATES.SUCCEEDED || status === JOB_STATES.FAILED || status === JOB_STATES.EXPIRED
}

// Whether a state is active (polling).
export function isActive(status) {
  return status === JOB_STATES.QUEUED || status === JOB_STATES.RUNNING
}

// Format an expiresAt timestamp as a readable countdown/absolute time.
export function formatExpiry(expiresAt) {
  if (!expiresAt) return null
  const date = new Date(expiresAt)
  if (isNaN(date.getTime())) return null
  const now = Date.now()
  const remaining = date.getTime() - now
  if (remaining <= 0) return 'expired'
  const minutes = Math.floor(remaining / 60000)
  const seconds = Math.floor((remaining % 60000) / 1000)
  if (minutes > 0) return `${minutes}m ${seconds}s remaining`
  return `${seconds}s remaining`
}

// Format a job spec for display.
export function formatJobSpec(spec) {
  if (!spec) return null
  return {
    range: `${millisToDuration(spec.fromMs)} – ${millisToDuration(spec.toMs)}`,
    duration: millisToDuration(spec.clipDuration),
    subtitle: spec.subtitleLabel,
    audioMode: audioModeLabel(spec.audioMode),
    subtitleOffsetMs: clampSubtitleOffsetMs(spec?.subtitleOffsetMs),
    subtitleOffsetLabel: `Offset ${formatSubtitleOffsetMs(spec?.subtitleOffsetMs)}`,
  }
}
