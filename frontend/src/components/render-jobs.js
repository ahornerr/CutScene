import {millisToDuration} from "../utils";

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
export function buildRenderJobRequest(session, startPosition, endPosition, selectedSubtitle, audioMode) {
  const mediaId = normalizeMediaId(session?.Media?.[0]?.Part?.[0]?.id)
  if (mediaId == null) throw new Error('The selected media part has an invalid media ID.')
  return {
    ratingKey: session.ratingKey,
    mediaId,
    fromMs: startPosition,
    toMs: endPosition,
    subtitleIndex: selectedSubtitle,
    audioMode: audioMode || AUDIO_MODES.STANDARD,
  }
}

// Build a request body from an immutable submitted spec (for re-render).
export function buildRenderJobRequestFromSpec(spec) {
  const mediaId = normalizeMediaId(spec?.mediaId)
  if (mediaId == null) throw new Error('The submitted render job has an invalid media ID.')
  return {
    ratingKey: spec.ratingKey,
    mediaId,
    fromMs: spec.fromMs,
    toMs: spec.toMs,
    subtitleIndex: spec.subtitleIndex,
    audioMode: spec.audioMode || AUDIO_MODES.STANDARD,
  }
}

// Plex may return Part ids as strings. Keep the API payload numeric and reject
// malformed ids before a request reaches the backend's int64 decoder.
export function normalizeMediaId(value) {
  if (value == null || (typeof value === 'string' && value.trim() === '')) return null
  const numeric = Number(value)
  return Number.isFinite(numeric) && Number.isInteger(numeric) ? numeric : null
}

// Whether an error code is a transport/polling issue (not a render failure).
export function isTransportError(error) {
  if (!error) return false
  return ['network_error', 'service_unavailable', 'auth_error', 'request_error'].includes(error.code)
}

// Snapshot an immutable submitted spec for display. This is captured at job
// creation time and never mutated — it represents what was actually submitted,
// not the current (possibly edited) controls.
export function snapshotJobSpec(session, startPosition, endPosition, selectedSubtitle, streams, audioMode) {
  const stream = selectedSubtitle >= 0 ? streams.find(s => s.index === selectedSubtitle) : null
  return {
    ratingKey: session.ratingKey,
    mediaId: normalizeMediaId(session?.Media?.[0]?.Part?.[0]?.id),
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
  }
}