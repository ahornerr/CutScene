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
export function buildRenderJobRequest(session, startPosition, endPosition, selectedSubtitle, audioMode, subtitleOffsetMs) {
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
export function snapshotJobSpec(session, startPosition, endPosition, selectedSubtitle, streams, audioMode, subtitleOffsetMs) {
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