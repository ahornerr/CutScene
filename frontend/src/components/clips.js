// Clip library API helpers and small presentation utilities.
//
// The backend clip API (see clips.go / api.go) exposes:
//   GET    /clips            -> {clips: [clipAPIResponse], isAdmin: bool}
//   GET    /clips/:id        ->  clipAPIResponse
//   GET    /clips/:id/download -> mp4 (attachment; auth required)
//   GET    /clips/:id/artwork  -> artwork image (auth required)
//   DELETE /clips/:id        -> 204 (owner or admin only; 404 otherwise)
//   GET    /shared/clips/:token        -> public clipAPIResponse (no ownerUuid)
//   GET    /shared/clips/:token/download -> mp4 (inline; public capability)
//
// clipAPIResponse fields: id, creatorDisplayName (optional on old clips),
// mediaKind, movieTitle, movieYear, showTitle, seasonNumber, episodeNumber,
// episodeTitle, artworkUrl, title, ratingKey, mediaId, fromMs, toMs,
// createdAt, shareUrl, downloadUrl, publicDownloadUrl, canDelete, isAdmin.
//
// `isAdmin` is an explicit boolean on the list response and a *bool on detail.
// `canDelete` is an explicit boolean per clip. `creatorDisplayName` is the
// friendly creator identity for admin differentiation; the backend no longer
// serializes ownerUuid, so the UI never derives a label from a UUID or email.
// `artworkUrl` is the authenticated durable artwork endpoint (/clips/:id/artwork).
// `shareUrl` and `publicDownloadUrl` are the same stable public inline MP4 URL.

// Authenticated list response: {clips: [...], isAdmin: bool}.
export async function fetchClips({signal} = {}) {
  const resp = await fetch('/clips', {redirect: 'manual', signal})
  if (resp.status === 401 || resp.status === 403 || resp.type === 'opaqueredirect') {
    const err = new Error('authentication required')
    err.code = 'auth'
    throw err
  }
  if (!resp.ok) {
    const err = new Error(`Could not load clips (${resp.status})`)
    err.code = 'network'
    throw err
  }
  const data = await resp.json()
  return {
    clips: Array.isArray(data?.clips) ? data.clips : [],
    isAdmin: Boolean(data?.isAdmin),
  }
}

export async function fetchClip(id, {signal} = {}) {
  const resp = await fetch(`/clips/${encodeURIComponent(id)}`, {redirect: 'manual', signal})
  if (resp.status === 401 || resp.status === 403 || resp.type === 'opaqueredirect') {
    const err = new Error('authentication required')
    err.code = 'auth'
    throw err
  }
  if (resp.status === 404) {
    const err = new Error('clip not found')
    err.code = 'not_found'
    throw err
  }
  if (!resp.ok) {
    const err = new Error(`Could not load this clip (${resp.status})`)
    err.code = 'network'
    throw err
  }
  return resp.json()
}

export async function deleteClip(id, {signal} = {}) {
  const resp = await fetch(`/clips/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    redirect: 'manual',
    signal,
  })
  if (resp.status === 204) return true
  if (resp.status === 401 || resp.status === 403 || resp.type === 'opaqueredirect') {
    const err = new Error('authentication required')
    err.code = 'auth'
    throw err
  }
  if (resp.status === 404) {
    const err = new Error('clip not found')
    err.code = 'not_found'
    throw err
  }
  // 503 / 5xx and anything else: treat as a transient server error so the
  // confirmation flow can offer a retry without closing the dialog.
  const err = new Error('Could not delete this clip right now.')
  err.code = 'network'
  throw err
}

// Copy text to the clipboard with a graceful fallback for non-secure contexts
// (jsdom / older browsers). Returns true on success.
export async function copyToClipboard(text) {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // fall through to the legacy path
    }
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.setAttribute('readonly', '')
    ta.style.position = 'fixed'
    ta.style.top = '0'
    ta.style.left = '0'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    let ok = false
    try { ok = document.execCommand('copy') } catch { ok = false }
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}

export function clipDurationMs(clip) {
  if (!clip) return 0
  const from = Number(clip.fromMs) || 0
  const to = Number(clip.toMs) || 0
  return Math.max(0, to - from)
}

export function formatClipDuration(clip) {
  return millisToHMS(clipDurationMs(clip))
}

export function millisToHMS(millis) {
  const total = Math.max(0, Math.floor(millis))
  const hours = Math.floor(total / 3600000)
  const minutes = Math.floor((total % 3600000) / 60000)
  const seconds = Math.floor((total % 60000) / 1000)
  return [hours, minutes, seconds]
    .map(n => String(n).padStart(2, '0'))
    .join(':')
}

export function formatClipCreated(clip) {
  const raw = clip && clip.createdAt
  if (!raw) return ''
  const date = new Date(raw)
  if (isNaN(date.getTime())) return ''
  // Locale-aware date with a short relative hint. Keep it compact for rows.
  const now = new Date()
  const sameDay = date.toDateString() === now.toDateString()
  if (sameDay) {
    return `Today, ${date.toLocaleTimeString([], {hour: '2-digit', minute: '2-digit'})}`
  }
  const yest = new Date(now)
  yest.setDate(now.getDate() - 1)
  if (date.toDateString() === yest.toDateString()) {
    return `Yesterday, ${date.toLocaleTimeString([], {hour: '2-digit', minute: '2-digit'})}`
  }
  return date.toLocaleDateString([], {year: 'numeric', month: 'short', day: 'numeric'})
}

// Creator display name for admin differentiation. The API exposes a friendly
// creatorDisplayName (Plex username/title) when available; old clips may not
// carry one. The backend no longer serializes ownerUuid, so never derive a
// label from a UUID or email — use a neutral "Unknown creator" when absent.
export function creatorLabel(clip) {
  const name = clip && clip.creatorDisplayName
  if (name && String(name).trim()) return String(name).trim()
  return 'Unknown creator'
}

// Build a structured media label from the durable clip metadata fields. The
// backend captures movie vs episode kind at promotion time so the label is
// stable across restarts and doesn't depend on live Plex metadata. Old clips
// may have partial or absent metadata.
//
// Returns {primary, secondary} where primary is the movie title or show name,
// and secondary is "(year)" for movies or "S##E## episodeTitle" for episodes.
// Either or both may be empty — the UI renders the context line whenever
// either part exists. Does NOT fall back to clip.title (the user-given clip
// name shown elsewhere as the headline); the media context line only appears
// when real media metadata is present.
export function mediaLabel(clip) {
  if (!clip) return {primary: '', secondary: ''}
  const kind = String(clip.mediaKind || '').toLowerCase()
  if (kind === 'episode') {
    return episodeLabel(clip)
  }
  if (kind === 'movie') {
    return movieLabel(clip)
  }
  // Unknown/absent kind: prefer movie fields, then episode fields.
  if (clip.movieTitle) return movieLabel(clip)
  if (clip.showTitle) return episodeLabel(clip)
  return {primary: '', secondary: ''}
}

// A single-line media context string for compact layouts (rows). Joins the
// primary and secondary with a middle dot, e.g. "Inception · (2010)" or
// "The Show · S02E07 Pilot". Returns '' only when BOTH parts are empty.
export function mediaContext(clip) {
  const {primary, secondary} = mediaLabel(clip)
  return [primary, secondary].filter(Boolean).join(' · ')
}

function movieLabel(clip) {
  const primary = clip.movieTitle || ''
  const year = clip.movieYear != null ? Number(clip.movieYear) : null
  const secondary = (Number.isFinite(year) && year > 0) ? `(${year})` : ''
  return {primary, secondary}
}

function episodeLabel(clip) {
  const primary = clip.showTitle || ''
  const season = clip.seasonNumber != null ? Number(clip.seasonNumber) : null
  const episode = clip.episodeNumber != null ? Number(clip.episodeNumber) : null
  const parts = []
  if (Number.isFinite(season) && season >= 0) {
    parts.push(`S${String(season).padStart(2, '0')}`)
  }
  if (Number.isFinite(episode) && episode >= 0) {
    parts.push(`E${String(episode).padStart(2, '0')}`)
  }
  const code = parts.join('')
  const epTitle = clip.episodeTitle || ''
  const secondary = [code, epTitle].filter(Boolean).join(' ')
  return {primary, secondary}
}