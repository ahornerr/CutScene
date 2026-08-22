import './App.css';
import {useCallback, useEffect, useMemo, useRef, useState} from "react";
import {
  Box, Button, Container, Stack, Toolbar, Typography,
} from "@mui/material";
import LibrarySearchPanel from "./components/LibrarySearchPanel";
import ClipWorkspace from "./components/ClipWorkspace";
import ClipLibrary from "./components/ClipLibrary";
import ClipDetail from "./components/ClipDetail";
import SemanticSubtitleSearch from "./components/SemanticSubtitleSearch";
import {orderSubtitleStreams} from "./components/subtitle-formats";
import {stripSubtitleMarkup} from "./components/subtitle-markup";
import {
  buildRenderJobRequest, buildRenderJobRequestFromSpec, isTerminal, isActive, JOB_STATES, snapshotJobSpec, AUDIO_MODES, RENDER_RESOLUTIONS,
  getSourcePartId, validateLibraryResult, getSafeResolution,
} from "./components/render-jobs";
import {
  isAbortError, millisToDuration, MIN_GAP_MS,
} from "./utils";

// Normalize a backend LibrarySearchResult into the session-shaped object the
// workspace expects. The shape mirrors an active Plex session (ratingKey,
// Media[].Part[].id, duration, title/hierarchy) so the existing preview /
// subtitle / trim flows work unchanged. Two underscore-prefixed fields tag the
// source kind so request builders can append `partId` only for library sources:
//   _sourceType: 'library'
//   _partId:    numeric part id from the search result
// Active sessions carry neither field, so they resolve through the legacy path.
function libraryResultToSession(result) {
  return {
    ratingKey: result.ratingKey,
    type: result.type,
    title: result.title,
    grandparentTitle: result.grandparentTitle || '',
    parentTitle: result.parentTitle || '',
    parentIndex: result.seasonNumber ?? null,
    index: result.episodeNumber ?? null,
    year: result.year ?? null,
    duration: result.duration,
    // Library items have no live view offset — clips start at the beginning.
    viewOffset: 0,
    thumb: result.artwork || '',
    Media: [{Part: [{id: String(result.mediaId)}], height: result.height, videoResolution: result.videoResolution || '', videoCodec: result.videoCodec || '', videoProfile: result.videoProfile || '', audioChannels: result.audioChannels || 0, audioCodec: result.audioCodec || ''}],
    _sourceType: 'library',
    _partId: result.partId,
  }
}

// External media uses the same workspace/session shape as Plex so the existing
// trim, subtitle, preview, and player components can be reused. Render request
// construction still keys off _sourceType and sends sourceId as an external ID.
function externalSourceToSession(source) {
  const sourceId = source?.sourceId == null ? '' : String(source.sourceId).trim()
  if (!sourceId) return null
  return {
    ...source,
    sourceId,
    title: source.title || 'YouTube video',
    type: source.type || 'movie',
    duration: Number(source.duration) || 0,
    viewOffset: 0,
    thumb: source.thumb || source.artwork || '',
    // Compatibility fields for the existing workspace data flow. They are not
    // used by the external render request, which deliberately omits Plex IDs.
    ratingKey: source.ratingKey || `youtube:${sourceId}`,
    Media: source.Media || [{Part: [{id: String(source.mediaId || sourceId)}]}],
    _sourceType: 'youtube',
  }
}

// Build the optional `&partId=` query segment for explicit library sources.
// Active sessions return null here so the legacy request shape is preserved.
function partIdParam(session) {
  const partId = getSourcePartId(session)
  return partId != null ? `&partId=${partId}` : ''
}

// Safely parse a fetch response expected to be a JSON array. The backend's
// error envelopes ({error: {code, message}}) and non-2xx responses are turned
// into typed errors instead of being stored where an array is expected —
// downstream .filter/.map calls would otherwise crash on a non-array shape.
// Returns [] for a 2xx response whose body isn't an array, so the UI degrades
// to an empty state rather than a render crash.
async function safeJsonArray(response) {
  if (!response.ok) {
    let body = null
    try { body = await response.json() } catch { /* non-JSON or empty */ }
    const message = body?.error?.message || `Request failed (${response.status}).`
    const err = new Error(message)
    err.status = response.status
    err.code = body?.error?.code || 'request_error'
    throw err
  }
  let data
  try { data = await response.json() }
  catch (e) {
    const err = new Error('Received a malformed response from the server.')
    err.code = 'malformed_response'
    throw err
  }
  return Array.isArray(data) ? data : []
}

const POLL_INTERVAL_MS = 2000
const SESSION_REFRESH_INTERVAL_MS = 10000
const MAX_NETWORK_RETRIES = 5
const EXPIRY_TICK_MS = 1000

function viewFromHash(hash) {
  if (!hash || hash === '#' || hash === '#/') return {name: 'home'}
  if (hash === '#/clips') return {name: 'library'}
  if (hash === '#/subtitle-search') return {name: 'subtitleSearch'}

  const externalWorkspaceMatch = hash.match(/^#\/workspace\/(youtube|external)\/([^/?#]+)$/)
  if (externalWorkspaceMatch) {
    try {
      const sourceId = decodeURIComponent(externalWorkspaceMatch[2])
      if (sourceId) return {name: 'workspace', sourceType: externalWorkspaceMatch[1] === 'youtube' ? 'youtube' : 'external', sourceId}
    } catch {
      // Fall through to the canonical home route for malformed escapes.
    }
    return {name: 'home'}
  }

  const workspaceMatch = hash.match(/^#\/workspace\/([^/?#]+)\?mediaId=([1-9]\d*)(?:&partId=([1-9]\d*))?$/)
  if (workspaceMatch) {
    try {
      const ratingKey = decodeURIComponent(workspaceMatch[1])
      const mediaId = Number(workspaceMatch[2])
      const partId = workspaceMatch[3] == null ? null : Number(workspaceMatch[3])
      if (ratingKey && isPositiveSafeInteger(mediaId) && (partId == null || isPositiveSafeInteger(partId))) {
        return {name: 'workspace', ratingKey, mediaId, partId}
      }
    } catch {
      // Fall through to the canonical home route for malformed escapes.
    }
    return {name: 'home'}
  }

  const clipPrefix = '#/clips/'
  if (!hash.startsWith(clipPrefix)) return {name: 'home'}

  const encodedClipId = hash.slice(clipPrefix.length)
  if (!encodedClipId || encodedClipId.includes('/')) return {name: 'home'}
  try {
    const clipId = decodeURIComponent(encodedClipId)
    return clipId ? {name: 'clip', clipId} : {name: 'home'}
  } catch {
    return {name: 'home'}
  }
}

function isPositiveSafeInteger(value) {
  return Number.isSafeInteger(value) && value > 0
}

function hashForView(view) {
  if (view.name === 'library') return '#/clips'
  if (view.name === 'subtitleSearch') return '#/subtitle-search'
  if (view.name === 'clip' && view.clipId) return `#/clips/${encodeURIComponent(view.clipId)}`
  if (view.name === 'workspace' && view.sourceId) {
    return `#/workspace/${view.sourceType === 'youtube' ? 'youtube' : 'external'}/${encodeURIComponent(view.sourceId)}`
  }
  if (view.name === 'workspace' && view.ratingKey && isPositiveSafeInteger(view.mediaId)) {
    const partParam = view.partId != null && isPositiveSafeInteger(view.partId) ? `&partId=${view.partId}` : ''
    return `#/workspace/${encodeURIComponent(view.ratingKey)}?mediaId=${view.mediaId}${partParam}`
  }
  return '#/'
}

function workspaceViewForSession(session) {
  if (session?._sourceType === 'youtube') {
    const sourceId = String(session.sourceId || '').trim()
    return sourceId ? {name: 'workspace', sourceType: 'youtube', sourceId} : null
  }
  const ratingKey = session?.ratingKey
  const mediaId = Number(session?.Media?.[0]?.Part?.[0]?.id)
  if (typeof ratingKey !== 'string' || !ratingKey || !isPositiveSafeInteger(mediaId)) return null
  const view = {name: 'workspace', ratingKey, mediaId, partId: null}
  if (session?._sourceType === 'library') {
    const partId = Number(session._partId)
    if (!isPositiveSafeInteger(partId)) return null
    view.partId = partId
  }
  return view
}

function sessionMatchesWorkspace(session, workspace) {
  if (workspace?.sourceId) {
    return session?._sourceType === 'youtube' && String(session.sourceId) === String(workspace.sourceId)
  }
  if (!session || !workspace || String(session.ratingKey) !== workspace.ratingKey) return false
  const mediaId = Number(session?.Media?.[0]?.Part?.[0]?.id)
  if (mediaId !== workspace.mediaId) return false
  if (workspace.partId == null) return session._sourceType !== 'library'
  return session._sourceType === 'library' && Number(session._partId) === workspace.partId
}

function authRequiredError() {
  const error = new Error('Authentication required.')
  error.code = 'auth'
  return error
}

async function fetchWorkspaceSessions(signal) {
  const response = await fetch('/sessions', {redirect: 'manual', signal})
  if (response.type === 'opaqueredirect' || response.status === 401 || response.status === 403) {
    throw authRequiredError()
  }
  return safeJsonArray(response)
}

function App() {
  // --- Navigation view ---
  // Smallest suitable routing: an in-app view state instead of a router
  // dependency. 'home' renders the session picker / active workspace; 'library'
  // renders the saved-clip library; 'clip' renders a single clip detail. The
  // workspace state (selectedSession, render job, etc.) is retained across
  // navigation so the render workflow is never disrupted.
  const [view, setView] = useState(() => viewFromHash(window.location.hash))
  const activeJobRef = useRef(false)
  const selectedSessionRef = useRef(null)

  // Hash navigation is deliberately kept outside the workspace state. The
  // browser owns the history entries, while this listener makes back/forward
  // navigation update the rendered view without disturbing the workspace.
  useEffect(() => {
    const syncViewToHash = () => {
      const nextView = viewFromHash(window.location.hash)

      // An active render owns its source workspace. History navigation must
      // not hydrate a different source over it or strand the running job.
      const activeSession = selectedSessionRef.current
      if (
        activeJobRef.current &&
        nextView.name === 'workspace' &&
        activeSession &&
        !sessionMatchesWorkspace(activeSession, nextView)
      ) {
        const canonicalView = workspaceViewForSession(activeSession)
        if (canonicalView) {
          setView(canonicalView)
          window.history.replaceState(null, '', hashForView(canonicalView))
          return
        }
      }

      setView(nextView)

      // Invalid routes resolve to the canonical home URL without adding a
      // second history entry for the malformed hash.
      const canonicalHash = hashForView(nextView)
      if (window.location.hash !== canonicalHash) {
        window.history.replaceState(null, '', canonicalHash)
      }
    }

    syncViewToHash()
    window.addEventListener('hashchange', syncViewToHash)
    window.addEventListener('popstate', syncViewToHash)
    return () => {
      window.removeEventListener('hashchange', syncViewToHash)
      window.removeEventListener('popstate', syncViewToHash)
    }
  }, [])

  // --- Session list ---
  const [sessions, setSessions] = useState(null)
  const [sessionsError, setSessionsError] = useState(null)
  const [needsAuth, setNeedsAuth] = useState(false)

  // --- Active clip state ---
  const [selectedSession, setSelectedSession] = useState(null)
  selectedSessionRef.current = selectedSession
  const [startPosition, setStartPosition] = useState(null)
  const [endPosition, setEndPosition] = useState(null)
  const [playerUrl, setPlayerUrl] = useState(null)
  const [playerError, setPlayerError] = useState(false)
  const [previewStale, setPreviewStale] = useState(false)
  const [audioMode, setAudioMode] = useState(AUDIO_MODES.STANDARD)
  const [resolution, setResolution] = useState(RENDER_RESOLUTIONS.NATIVE)

  // --- Theater mode ---
  // Desktop-only view toggle: widens the preview rail (Container lg → xl) and
  // collapses the workspace grid to a single column so subtitles drop beneath
  // the player. Reset on session change alongside the other ephemeral
  // workspace state for a predictable per-session default.
  const [theaterMode, setTheaterMode] = useState(false)
  const [autoPlayFromSearch, setAutoPlayFromSearch] = useState(false)

  // --- Subtitle offset ---
  // Positive = subtitles appear later, negative = earlier. Retained across
  // subtitle track switches and entry picks (not reset by selection); reset
  // only on session change, mirroring audioMode. Applied to the preview URL,
  // the render-job payload (as `subtitleOffsetMs`), and subtitle-derived clip
  // range math.
  const [subtitleOffsetMs, setSubtitleOffsetMs] = useState(0)

  // --- Subtitle streams ---
  const [subtitleStreams, setSubtitleStreams] = useState([])
  const [selectedSubtitle, setSelectedSubtitle] = useState(-1)
  const [streamsError, setStreamsError] = useState(null)
  const [streamsLoading, setStreamsLoading] = useState(false)

  // --- Subtitle entries ---
  const [subtitleEntries, setSubtitleEntries] = useState([])
  const [subtitleSearch, setSubtitleSearch] = useState('')
  const [subtitlesLoading, setSubtitlesLoading] = useState(false)
  const [subtitlesError, setSubtitlesError] = useState(null)
  const [subtitleAnchor, setSubtitleAnchor] = useState(-1)
  const [subtitleSelectionEnd, setSubtitleSelectionEnd] = useState(-1)

  // --- Render job ---
  const [renderState, setRenderState] = useState({status: null, error: null, downloadUrl: null, expiresAt: null, retryAfter: null, clipId: null, shareUrl: null})
  const [jobSpec, setJobSpec] = useState(null)
  const [downloadCompleted, setDownloadCompleted] = useState(false)
  const [controlsChangedSinceJob, setControlsChangedSinceJob] = useState(false)
  const [expiryTick, setExpiryTick] = useState(0) // forces expiry countdown refresh

  // --- Trim flash ---
  const [trimFlashKey, setTrimFlashKey] = useState(0)

  const subtitleListRef = useRef(null)
  const sessionGenerationRef = useRef(0)
  const sessionAbortControllerRef = useRef(null)
  const playerReadyRef = useRef(false)
  const skipInitialSubtitlePreviewRef = useRef(false)
  const pollControllerRef = useRef(null)
  const pollTimeoutRef = useRef(null)
  const networkRetryCountRef = useRef(0)
  const jobIdRef = useRef(null)
  const sessionRefreshControllerRef = useRef(null)
  const sessionRefreshTimeoutRef = useRef(null)
  const sessionRefreshInFlightRef = useRef(false)
  const sessionRefreshGenerationRef = useRef(0)
  const sessionRefreshRequestRef = useRef(0)
  const sessionRefreshActiveRef = useRef(false)
  const sessionRefreshRunnerRef = useRef(null)
  const sessionsHaveDataRef = useRef(false)
  const workspaceHydrationControllerRef = useRef(null)
  // Search ranges are intentionally transient. The workspace URL remains its
  // established source-only deep link, while this ref carries one selected
  // subtitle moment through source hydration without polluting history.
  const pendingSubtitleRangeRef = useRef(null)
  const searchPlaybackWorkspaceRef = useRef(null)

  // ---------------------------------------------------------------- render-job cleanup
  const stopPolling = useCallback(() => {
    if (pollTimeoutRef.current) {
      clearTimeout(pollTimeoutRef.current)
      pollTimeoutRef.current = null
    }
    if (pollControllerRef.current) {
      pollControllerRef.current.abort()
      pollControllerRef.current = null
    }
    networkRetryCountRef.current = 0
  }, [])

  const resetRenderState = useCallback(() => {
    stopPolling()
    setRenderState({status: null, error: null, downloadUrl: null, expiresAt: null, retryAfter: null, clipId: null, shareUrl: null})
    setJobSpec(null)
    jobIdRef.current = null
    setDownloadCompleted(false)
    setControlsChangedSinceJob(false)
  }, [stopPolling])

  // ---------------------------------------------------------------- clamp helper
  const clampToBounds = useCallback((session, start, end) => {
    if (start == null || end == null) return [start, end]
    const duration = session?.duration ?? Infinity
    let s = Math.max(0, Math.min(start, duration))
    let e = Math.max(0, Math.min(end, duration))
    if (e - s < MIN_GAP_MS) {
      if (e >= duration) s = Math.max(0, duration - MIN_GAP_MS)
      else e = s + MIN_GAP_MS
    }
    return [s, e]
  }, [])

  // ---------------------------------------------------------------- sessions
  const [sessionsRetry, setSessionsRetry] = useState(0)
  const [documentVisible, setDocumentVisible] = useState(() => document.visibilityState !== 'hidden')

  const stopSessionRefresh = useCallback(() => {
    sessionRefreshActiveRef.current = false
    sessionRefreshGenerationRef.current += 1
    if (sessionRefreshTimeoutRef.current) {
      clearTimeout(sessionRefreshTimeoutRef.current)
      sessionRefreshTimeoutRef.current = null
    }
    if (sessionRefreshControllerRef.current) {
      sessionRefreshControllerRef.current.abort()
      sessionRefreshControllerRef.current = null
    }
    sessionRefreshInFlightRef.current = false
  }, [])

  const scheduleSessionRefresh = useCallback(() => {
    if (!sessionRefreshActiveRef.current || sessionRefreshInFlightRef.current || sessionRefreshTimeoutRef.current) return
    sessionRefreshTimeoutRef.current = setTimeout(() => {
      sessionRefreshTimeoutRef.current = null
      sessionRefreshRunnerRef.current?.()
    }, SESSION_REFRESH_INTERVAL_MS)
  }, [])

  const refreshSessions = useCallback(() => {
    if (!sessionRefreshActiveRef.current || sessionRefreshInFlightRef.current) return

    const generation = sessionRefreshGenerationRef.current
    const requestId = sessionRefreshRequestRef.current + 1
    sessionRefreshRequestRef.current = requestId
    const controller = new AbortController()
    sessionRefreshControllerRef.current = controller
    sessionRefreshInFlightRef.current = true

    const isCurrent = () => (
      sessionRefreshActiveRef.current &&
      sessionRefreshGenerationRef.current === generation &&
      sessionRefreshRequestRef.current === requestId
    )

    fetch('/sessions', {redirect: "manual", signal: controller.signal})
      .then(response => {
        if (!isCurrent()) return undefined
        if (response.type === "opaqueredirect" || response.status === 401 || response.status === 403) {
          setNeedsAuth(true)
          stopSessionRefresh()
          return undefined
        }
        if (!response.ok) {
          throw new Error(`Error fetching sessions: ${response.status} ${response.statusText}`)
        }
        return response.json()
      })
      .then(json => {
        if (!isCurrent() || json === undefined) return
        sessionsHaveDataRef.current = true
        setSessions(json || [])
        setSessionsError(null)
      })
      .catch(err => {
        if (!isCurrent() || isAbortError(err)) return
        console.error('Error refreshing sessions:', err)
        // Refresh failures retain the last good picker data. Only the initial
        // load uses the error state so the existing retry UX remains intact.
        if (!sessionsHaveDataRef.current) setSessionsError(err)
      })
      .finally(() => {
        if (sessionRefreshGenerationRef.current !== generation || sessionRefreshRequestRef.current !== requestId) return
        sessionRefreshInFlightRef.current = false
        if (sessionRefreshControllerRef.current === controller) sessionRefreshControllerRef.current = null
        scheduleSessionRefresh()
      })
  }, [scheduleSessionRefresh, stopSessionRefresh])

  sessionRefreshRunnerRef.current = refreshSessions

  useEffect(() => {
    const handleVisibilityChange = () => setDocumentVisible(document.visibilityState !== 'hidden')
    document.addEventListener('visibilitychange', handleVisibilityChange)
    return () => document.removeEventListener('visibilitychange', handleVisibilityChange)
  }, [])

  useEffect(() => {
    // Session polling is gated to the home view: the library and clip detail
    // views don't need live session data, and polling in the background
    // would compete for the network and surface stale workspace alerts.
    const pickerVisible = view.name === 'home' && !needsAuth && documentVisible
    if (!pickerVisible) {
      stopSessionRefresh()
      return undefined
    }

    sessionRefreshActiveRef.current = true
    refreshSessions()
    return stopSessionRefresh
  }, [documentVisible, needsAuth, refreshSessions, selectedSession, sessionsRetry, stopSessionRefresh, view.name])

  const retrySessions = useCallback(() => {
    stopSessionRefresh()
    sessionsHaveDataRef.current = false
    setSessionsError(null)
    setSessions(null)
    setSessionsRetry(n => n + 1)
  }, [stopSessionRefresh])

  // ---------------------------------------------------------------- player URL builder
  const setPlayerPosition = useCallback((start, end, subtitleOverride = selectedSubtitle, audioModeOverride = audioMode, offsetOverride = subtitleOffsetMs) => {
    const subtitleIndex = subtitleOverride
    const subtitleParam = subtitleIndex >= 0 ? `&subtitle=${subtitleIndex}` : ''
    const audioModeParam = audioModeOverride && audioModeOverride !== AUDIO_MODES.STANDARD
      ? `&audioMode=${audioModeOverride}`
      : ''
    // Only emit the offset param when a subtitle track is selected and the
    // offset is non-zero — keeps the default preview URL stable.
    const offsetParam = (subtitleIndex >= 0 && offsetOverride) ? `&subtitleOffsetMs=${offsetOverride}` : ''
    setPlayerError(false)
    setPlayerUrl(
      `/preview/${selectedSession.ratingKey}/${millisToDuration(start)}/${millisToDuration(end)}` +
      `?mediaId=${selectedSession.Media[0].Part[0].id}${partIdParam(selectedSession)}${subtitleParam}${audioModeParam}${offsetParam}`
    )
  }, [selectedSession, selectedSubtitle, audioMode, subtitleOffsetMs])

  // ---------------------------------------------------------------- session change → streams
  useEffect(() => {
    const generation = sessionGenerationRef.current + 1
    sessionGenerationRef.current = generation
    sessionAbortControllerRef.current?.abort()

    const controller = new AbortController()
    sessionAbortControllerRef.current = controller
    playerReadyRef.current = false
    skipInitialSubtitlePreviewRef.current = false

    if (selectedSession) {
      const [initStart, initEnd] = clampToBounds(selectedSession, selectedSession.viewOffset, selectedSession.viewOffset + 60000)
      const mediaId = selectedSession.Media[0].Part[0].id
      setPlayerError(false)
      setPreviewStale(false)

      setStartPosition(initStart)
      setEndPosition(initEnd)
      setAudioMode(AUDIO_MODES.STANDARD)
      setResolution(getSafeResolution(selectedSession?.Media?.[0]?.height))
      setSubtitleOffsetMs(0)
      setSubtitleStreams([])
      setSelectedSubtitle(-1)
      setStreamsError(null)
      setSubtitlesError(null)
      setStreamsLoading(true)

      resetRenderState()

      // Initial preview is independent of stream metadata and subtitle entries.
      playerReadyRef.current = true
      skipInitialSubtitlePreviewRef.current = true
      setPlayerPosition(initStart, initEnd, -1, AUDIO_MODES.STANDARD, 0)

      fetch(`/streams/${selectedSession.ratingKey}?mediaId=${encodeURIComponent(mediaId)}${partIdParam(selectedSession)}`, {signal: controller.signal})
        .then(safeJsonArray)
        .then(streams => {
          if (controller.signal.aborted || sessionGenerationRef.current !== generation) return
          const availableStreams = orderSubtitleStreams(streams)
          const pendingSearchSelection = pendingSubtitleRangeRef.current
          const requestedStreamIndex = Number(pendingSearchSelection?.subtitleIndex)
          const selectedStreamIndex = availableStreams.some(stream => stream.index === requestedStreamIndex)
            ? requestedStreamIndex
            : availableStreams.length > 0 ? availableStreams[0].index : -1
          setSubtitleStreams(availableStreams)
          setStreamsLoading(false)
          playerReadyRef.current = true
          if (selectedStreamIndex >= 0) {
            setSelectedSubtitle(selectedStreamIndex)
          }
        })
        .catch(err => {
          if (!controller.signal.aborted && !isAbortError(err) && sessionGenerationRef.current === generation) {
            console.log('Could not fetch subtitle streams:', err)
            playerReadyRef.current = true
            setStreamsError(err)
            setStreamsLoading(false)
          }
        })
    }

    return () => {
      controller.abort()
      if (sessionAbortControllerRef.current === controller) {
        sessionAbortControllerRef.current = null
      }
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedSession])

  // Apply a subtitle-search range only after its source is the active
  // workspace. Identity matching prevents a stale pending click from changing
  // a later workspace, and clearing before state writes makes this one-shot.
  useEffect(() => {
    const pending = pendingSubtitleRangeRef.current
    if (!pending || !selectedSession || !sessionMatchesWorkspace(selectedSession, pending.workspace)) return
    // Let source stream discovery validate the requested subtitle index before
    // applying the hit range; otherwise the normal first-track default wins.
    if (streamsLoading) return
    pendingSubtitleRangeRef.current = null
    const [start, end] = clampToBounds(selectedSession, pending.startMs, pending.endMs)
    const requestedSubtitle = Number(pending.subtitleIndex)
    const effectiveSubtitle = subtitleStreams.some(stream => stream.index === requestedSubtitle)
      ? requestedSubtitle
      : selectedSubtitle
    setStartPosition(start)
    setEndPosition(end)
    setPlayerPosition(start, end, effectiveSubtitle >= 0 ? effectiveSubtitle : -1)
    setPreviewStale(false)
    setTrimFlashKey(key => key + 1)
    setAutoPlayFromSearch(true)
  }, [clampToBounds, selectedSession, selectedSubtitle, setPlayerPosition, subtitleStreams, streamsLoading, view])

  // ---------------------------------------------------------------- subtitle entries fetch
  useEffect(() => {
    if (!selectedSession || selectedSubtitle < 0) {
      setSubtitleEntries([])
      setSubtitleSearch('')
      setSubtitlesLoading(false)
      setSubtitlesError(null)
      return
    }
    setSubtitlesLoading(true)
    setSubtitleEntries([])
    setSubtitlesError(null)
    setSubtitleAnchor(-1)
    setSubtitleSelectionEnd(-1)

    const generation = sessionGenerationRef.current
    const controller = new AbortController()
    const mediaId = selectedSession.Media[0].Part[0].id
    fetch(`/subtitles/${selectedSession.ratingKey}?subtitle=${selectedSubtitle}&mediaId=${mediaId}${partIdParam(selectedSession)}`, {
      signal: controller.signal,
    })
      .then(safeJsonArray)
      .then(entries => {
        if (controller.signal.aborted || sessionGenerationRef.current !== generation) return
        setSubtitleEntries(entries)
      })
      .catch(err => {
        if (!controller.signal.aborted && !isAbortError(err) && sessionGenerationRef.current === generation) {
          console.error('Could not fetch subtitle entries:', err)
          setSubtitlesError(err)
        }
      })
      .finally(() => {
        if (!controller.signal.aborted && sessionGenerationRef.current === generation) {
          setSubtitlesLoading(false)
        }
      })
    return () => controller.abort()
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedSession, selectedSubtitle])

  // ---------------------------------------------------------------- subtitle track auto-preview
  useEffect(() => {
    if (!selectedSession) return
    if (startPosition == null || endPosition == null) return
    if (!playerReadyRef.current) return
    if (selectedSubtitle < 0 && skipInitialSubtitlePreviewRef.current) {
      skipInitialSubtitlePreviewRef.current = false
      return
    }
    skipInitialSubtitlePreviewRef.current = false
    setPlayerPosition(startPosition, endPosition)
    setPreviewStale(false)
    setControlsChangedSinceJob(true)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedSubtitle])

  // ---------------------------------------------------------------- audio mode auto-preview
  // Changing audio mode deliberately refreshes the preview so the user can
  // hear the effect immediately — it's a discrete, intentional action like
  // changing the subtitle track, not a continuous trim adjustment.
  useEffect(() => {
    if (!selectedSession) return
    if (startPosition == null || endPosition == null) return
    if (!playerReadyRef.current) return
    setPlayerPosition(startPosition, endPosition)
    setPreviewStale(false)
    setControlsChangedSinceJob(true)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [audioMode])

  // ---------------------------------------------------------------- derived subtitle state
  const filteredSubtitleEntries = useMemo(() => {
    if (!subtitleSearch.trim()) return []
    const lower = subtitleSearch.toLowerCase()
    return subtitleEntries
      .map((e, idx) => ({...e, _fullIdx: idx}))
      .filter(e => stripSubtitleMarkup(e.text).toLowerCase().includes(lower))
  }, [subtitleEntries, subtitleSearch])

  const fullEntriesWithIdx = useMemo(
    () => subtitleEntries.map((e, idx) => ({...e, _fullIdx: idx})),
    [subtitleEntries]
  )

  const subtitleSelectionRange = useMemo(() => {
    if (subtitleAnchor < 0) return {from: -1, to: -1}
    const end = subtitleSelectionEnd >= 0 ? subtitleSelectionEnd : subtitleAnchor
    return {from: Math.min(subtitleAnchor, end), to: Math.max(subtitleAnchor, end)}
  }, [subtitleAnchor, subtitleSelectionEnd])

  useEffect(() => {
    if (subtitleAnchor < 0 || !subtitleListRef.current) return
    const el = subtitleListRef.current.querySelector(`[data-idx="${subtitleAnchor}"]`)
    if (el) el.scrollIntoView({block: 'center'})
  }, [subtitleAnchor])

  // ---------------------------------------------------------------- explicit preview apply
  const applyPreview = useCallback(() => {
    if (startPosition == null || endPosition == null) return
    if (!selectedSession) return
    setPlayerPosition(startPosition, endPosition)
    setPreviewStale(false)
  }, [startPosition, endPosition, selectedSession, setPlayerPosition])

  // ---------------------------------------------------------------- trim flash
  const flashTrim = useCallback(() => {
    setTrimFlashKey(k => k + 1)
  }, [])

  // ---------------------------------------------------------------- subtitle selection refit
  // Recompute the clip start/end around the active subtitle selection using
  // shifted timings (entry.start/end + offset) and the same 500ms padding +
  // duration clamping as a fresh pick. Returns null when no selection is
  // active (anchor < 0) or the entries are missing — callers must not alter a
  // manually-set range in that case.
  const refitSelectionRange = useCallback((offset) => {
    if (subtitleAnchor < 0) return null
    const duration = selectedSession?.duration ?? Infinity
    const end = subtitleSelectionEnd >= 0 ? subtitleSelectionEnd : subtitleAnchor
    const from = Math.min(subtitleAnchor, end)
    const to = Math.max(subtitleAnchor, end)
    const firstEntry = subtitleEntries[from]
    const lastEntry = subtitleEntries[to]
    if (!firstEntry || !lastEntry) return null
    const shiftedStart = firstEntry.start + offset
    const shiftedEnd = lastEntry.end + offset
    const newStart = Math.max(0, Math.min(shiftedStart - 500, duration))
    const newEnd = Math.max(0, Math.min(shiftedEnd + 500, duration))
    return [newStart, newEnd]
  }, [subtitleAnchor, subtitleSelectionEnd, subtitleEntries, selectedSession])

  // ---------------------------------------------------------------- subtitle offset auto-preview
  // Changing the subtitle offset is a discrete, intentional action. When a
  // subtitle entry or contiguous multi-selection is active, the clip range is
  // refit around the shifted selection (same 500ms padding/clamping as a pick)
  // before refreshing the preview. When no selection is active, a manually-set
  // range is left untouched and only the preview URL updates.
  useEffect(() => {
    if (!selectedSession) return
    if (startPosition == null || endPosition == null) return
    if (!playerReadyRef.current) return
    const refit = refitSelectionRange(subtitleOffsetMs)
    if (refit) {
      const [newStart, newEnd] = refit
      setStartPosition(newStart)
      setEndPosition(newEnd)
      setPlayerPosition(newStart, newEnd)
      flashTrim()
      setPreviewStale(false)
      setControlsChangedSinceJob(true)
      return
    }
    setPlayerPosition(startPosition, endPosition)
    setPreviewStale(false)
    setControlsChangedSinceJob(true)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [subtitleOffsetMs])

  // ---------------------------------------------------------------- bound setters (clamp to duration only)
  // A typed timestamp commit is a single discrete, intentional action — like
  // picking a subtitle or changing audio mode — so it auto-applies the preview
  // exactly once. The slider drag stays deferred behind the stale button
  // because it's continuous. BoundField.commit only calls onTimeChange for a
  // valid parse + gap, so intermediate invalid text never reaches here.
  const setBoundedStartPosition = useCallback((newValue) => {
    const duration = selectedSession?.duration ?? Infinity
    const clamped = Math.max(0, Math.min(newValue, duration))
    if (clamped <= endPosition - MIN_GAP_MS) {
      setStartPosition(clamped)
      setPlayerPosition(clamped, endPosition)
      setPreviewStale(false)
      setControlsChangedSinceJob(true)
    }
  }, [endPosition, selectedSession, setPlayerPosition])

  const setBoundedEndPosition = useCallback((newValue) => {
    const duration = selectedSession?.duration ?? Infinity
    const clamped = Math.max(0, Math.min(newValue, duration))
    if (clamped >= startPosition + MIN_GAP_MS) {
      setEndPosition(clamped)
      setPlayerPosition(startPosition, clamped)
      setPreviewStale(false)
      setControlsChangedSinceJob(true)
    }
  }, [startPosition, selectedSession, setPlayerPosition])

  const handleRangeChange = useCallback((s, e) => {
    const duration = selectedSession?.duration ?? Infinity
    const cs = Math.max(0, Math.min(s, duration))
    const ce = Math.max(0, Math.min(e, duration))
    if (ce - cs < MIN_GAP_MS) return
    setStartPosition(cs)
    setEndPosition(ce)
    setPlayerPosition(cs, ce)
    setPreviewStale(false)
    setControlsChangedSinceJob(true)
  }, [selectedSession, setPlayerPosition])

  // ---------------------------------------------------------------- subtitle pick (clamp to duration)
  // Subtitle timings are shifted by `subtitleOffsetMs` before deriving the clip
  // range: a positive offset makes the subtitle appear later in the output, so
  // the clip should cover the shifted window (with the usual 500ms padding).
  const handleSubtitlePick = useCallback((fullIdx, shiftKey) => {
    const duration = selectedSession?.duration ?? Infinity
    const offset = subtitleOffsetMs
    if (subtitleSearch.trim()) {
      const entry = subtitleEntries[fullIdx]
      if (!entry) return
      const shiftedStart = entry.start + offset
      const shiftedEnd = entry.end + offset
      const newStart = Math.max(0, Math.min(shiftedStart - 500, duration))
      const newEnd = Math.max(0, Math.min(shiftedEnd + 500, duration))
      setSubtitleAnchor(fullIdx)
      setSubtitleSelectionEnd(fullIdx)
      setStartPosition(newStart)
      setEndPosition(newEnd)
      setSubtitleSearch('')
      setPreviewStale(false)
      setPlayerPosition(newStart, newEnd)
      flashTrim()
      setControlsChangedSinceJob(true)
      return
    }
    let from, to
    if (shiftKey && subtitleAnchor >= 0) {
      from = Math.min(subtitleAnchor, fullIdx)
      to = Math.max(subtitleAnchor, fullIdx)
      setSubtitleSelectionEnd(fullIdx)
    } else {
      from = fullIdx
      to = fullIdx
      setSubtitleAnchor(fullIdx)
      setSubtitleSelectionEnd(fullIdx)
    }
    const firstEntry = subtitleEntries[from]
    const lastEntry = subtitleEntries[to]
    if (!firstEntry || !lastEntry) return
    const shiftedStart = firstEntry.start + offset
    const shiftedEnd = lastEntry.end + offset
    const newStart = Math.max(0, Math.min(shiftedStart - 500, duration))
    const newEnd = Math.max(0, Math.min(shiftedEnd + 500, duration))
    setStartPosition(newStart)
    setEndPosition(newEnd)
    setPreviewStale(false)
    setPlayerPosition(newStart, newEnd)
    flashTrim()
    setControlsChangedSinceJob(true)
  }, [subtitleSearch, subtitleAnchor, subtitleEntries, setPlayerPosition, flashTrim, selectedSession, subtitleOffsetMs])

  // ---------------------------------------------------------------- render-job create
  const createRenderJob = useCallback((retrySpec = null) => {
    if (!selectedSession || startPosition == null || endPosition == null) return
    stopPolling()
    setDownloadCompleted(false)
    networkRetryCountRef.current = 0

    const spec = retrySpec || snapshotJobSpec(selectedSession, startPosition, endPosition, selectedSubtitle, subtitleStreams, audioMode, subtitleOffsetMs, resolution)
    let body
    try {
      body = JSON.stringify(retrySpec
        ? buildRenderJobRequestFromSpec(retrySpec)
        : buildRenderJobRequest(selectedSession, startPosition, endPosition, selectedSubtitle, audioMode, subtitleOffsetMs, resolution))
    } catch (error) {
      setRenderState({
        status: JOB_STATES.FAILED,
        error: {code: 'validation_error', message: error?.message || 'The selected media part has an invalid media ID.', retryable: false},
        downloadUrl: null, expiresAt: null, retryAfter: null, clipId: null, shareUrl: null,
      })
      return
    }

    setJobSpec(spec)
    setControlsChangedSinceJob(false)
    setRenderState({status: JOB_STATES.QUEUED, error: null, downloadUrl: null, expiresAt: null, retryAfter: null, clipId: null, shareUrl: null})

    const controller = new AbortController()
    pollControllerRef.current = controller
    fetch('/render-jobs', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body,
      signal: controller.signal,
    })
      .then(r => {
        if (r.status === 202) return r.json()
        if (r.status === 429 || r.status === 503) {
          const retryAfter = parseInt(r.headers.get('Retry-After') || '5', 10)
          const e = new Error('The render queue is busy. Try again in a moment.')
          e.retryable = true
          e.retryAfter = retryAfter
          throw e
        }
        return r.json().then(body => {
          const e = new Error(body?.error?.message || 'Could not start rendering.')
          e.retryable = body?.error?.retryable || false
          throw e
        })
      })
      .then(job => {
        if (controller.signal.aborted) return
        jobIdRef.current = job.id
        setRenderState({
          status: job.status || JOB_STATES.QUEUED,
          error: null,
          downloadUrl: job.downloadUrl || null,
          expiresAt: job.expiresAt || null,
          retryAfter: null,
          clipId: job.clipId || null,
          shareUrl: job.shareUrl || null,
        })
        pollForJobRef.current(job.id, controller)
      })
      .catch(err => {
        if (controller.signal.aborted || isAbortError(err)) return
        setRenderState({
          status: JOB_STATES.FAILED,
          error: {code: 'request_error', message: err?.message || 'Could not start rendering.', retryable: err?.retryable || false},
          downloadUrl: null, expiresAt: null, retryAfter: err?.retryAfter || null, clipId: null, shareUrl: null,
        })
      })
  }, [selectedSession, startPosition, endPosition, selectedSubtitle, subtitleStreams, audioMode, subtitleOffsetMs, resolution, stopPolling])

  // ---------------------------------------------------------------- render-job polling (ref-bound)
  const pollForJobRef = useRef(null)
  useEffect(() => {
    pollForJobRef.current = (id, controller) => {
      const poll = () => {
        if (controller.signal.aborted) return
        fetch(`/render-jobs/${id}`, {signal: controller.signal})
          .then(r => {
            if (r.status === 404) throw new Error('__gone__')
            if (r.status === 410) return r.json().then(body => ({...body, status: JOB_STATES.EXPIRED}))
            if (r.status === 401) throw new Error('__auth__')
            if (r.status === 503) {
              const retryAfter = parseInt(r.headers.get('Retry-After') || '5', 10)
              const e = new Error('__service__')
              e.retryAfter = retryAfter
              throw e
            }
            if (!r.ok) {
              return r.json().then(body => {
                const e = new Error(body?.error?.message || 'Could not check render status.')
                throw e
              })
            }
            return r.json()
          })
          .then(job => {
            if (controller.signal.aborted) return
            networkRetryCountRef.current = 0
            setRenderState({
              status: job.status,
              error: job.error || null,
              downloadUrl: job.downloadUrl || null,
              expiresAt: job.expiresAt || null,
              retryAfter: null,
              clipId: job.clipId || null,
              shareUrl: job.shareUrl || null,
            })
            if (!isTerminal(job.status)) {
              pollTimeoutRef.current = setTimeout(poll, POLL_INTERVAL_MS)
            }
          })
          .catch(err => {
            if (controller.signal.aborted || isAbortError(err)) return
            if (err?.message === '__gone__') {
              setRenderState(prev => ({...prev, status: JOB_STATES.EXPIRED, error: null, downloadUrl: null, expiresAt: null, retryAfter: null}))
              return
            }
            if (err?.message === '__auth__') {
              setRenderState({
                status: JOB_STATES.FAILED,
                error: {code: 'auth_error', message: 'Authentication expired. Reload the page and sign in again.', retryable: false},
                downloadUrl: null, expiresAt: null, retryAfter: null, clipId: null, shareUrl: null,
              })
              return
            }
            if (err?.message === '__service__') {
              const retryAfter = err.retryAfter || 5
              networkRetryCountRef.current += 1
              if (networkRetryCountRef.current > MAX_NETWORK_RETRIES) {
                setRenderState({
                  status: JOB_STATES.FAILED,
                  error: {code: 'service_unavailable', message: 'The server is temporarily unavailable. Try again later.', retryable: true},
                  downloadUrl: null, expiresAt: null, retryAfter: null, clipId: null, shareUrl: null,
                })
                return
              }
              setRenderState(prev => ({...prev, error: {code: 'service_unavailable', message: 'The server is busy. Waiting to retry…', retryable: true}, retryAfter}))
              pollTimeoutRef.current = setTimeout(poll, retryAfter * 1000)
              return
            }
            // Generic network error — bounded retries.
            networkRetryCountRef.current += 1
            if (networkRetryCountRef.current > MAX_NETWORK_RETRIES) {
              setRenderState({
                status: JOB_STATES.FAILED,
                error: {code: 'network_error', message: 'Lost connection to the server. Check your network and try again.', retryable: true},
                downloadUrl: null, expiresAt: null, retryAfter: null, clipId: null, shareUrl: null,
              })
              return
            }
            const backoff = POLL_INTERVAL_MS * 2
            setRenderState(prev => ({
              ...prev,
              error: {code: 'network_error', message: 'Connection issue. Retrying…', retryable: true},
              retryAfter: Math.round(backoff / 1000),
            }))
            pollTimeoutRef.current = setTimeout(poll, backoff)
          })
      }
      poll()
    }
  }, [])

  // ---------------------------------------------------------------- retry render (from immutable spec — creates a new job)
  const retryRenderJob = useCallback(() => {
    if (jobSpec) createRenderJob(jobSpec)
  }, [createRenderJob, jobSpec])

  // ---------------------------------------------------------------- retry poll (re-checks the same job — for transport failures)
  const retryPollJob = useCallback(() => {
    const id = jobIdRef.current
    // A 429/503 while creating the job has no job id yet. Retry that POST
    // from the immutable spec instead of silently making the button inert.
    if (!id) {
      if (jobSpec) createRenderJob(jobSpec)
      return
    }
    // Re-check the same job status. If the job still exists, resume polling.
    const controller = new AbortController()
    pollControllerRef.current = controller
    networkRetryCountRef.current = 0
    setRenderState(prev => ({...prev, status: prev.status === JOB_STATES.FAILED ? JOB_STATES.QUEUED : prev.status, error: null, retryAfter: null}))
    pollForJobRef.current(id, controller)
  }, [createRenderJob, jobSpec])

  // ---------------------------------------------------------------- render-job download (revalidate before download)
  const handleDownloadJob = useCallback(() => {
    // If the job has expired client-side, don't follow the download link.
    if (renderState.status !== JOB_STATES.SUCCEEDED || !renderState.downloadUrl) return
    setDownloadCompleted(true)
  }, [renderState.status, renderState.downloadUrl])

  // ---------------------------------------------------------------- expiry timer
  // When a job is succeeded, tick every second to update the countdown and
  // transition to expired when the deadline passes.
  useEffect(() => {
    if (renderState.status !== JOB_STATES.SUCCEEDED || !renderState.expiresAt) return
    const expiresDate = new Date(renderState.expiresAt)
    if (isNaN(expiresDate.getTime())) return

    const tick = () => {
      setExpiryTick(t => t + 1)
      if (Date.now() >= expiresDate.getTime()) {
        // The transient download expires; the durable saved clip (clipId)
        // persists in the library and remains discoverable.
        setRenderState(prev => ({
          ...prev,
          status: JOB_STATES.EXPIRED,
          error: null,
          downloadUrl: null,
          expiresAt: null,
          retryAfter: null,
        }))
      }
    }

    const interval = setInterval(tick, EXPIRY_TICK_MS)
    return () => clearInterval(interval)
  }, [renderState.status, renderState.expiresAt])

  // ---------------------------------------------------------------- session switching (blocked during active job)
  const jobIsActive = isActive(renderState.status)
  activeJobRef.current = jobIsActive

  // ---------------------------------------------------------------- hash navigation
  const navigateTo = useCallback((nextView, replace = false) => {
    // Autoplay is a one-source intent from a subtitle hit, never a property of
    // ordinary workspace history navigation.
    if (!searchPlaybackWorkspaceRef.current || !sessionMatchesWorkspace({
      ratingKey: nextView.ratingKey,
      Media: [{Part: [{id: String(nextView.mediaId)}]}],
      _sourceType: nextView.partId != null ? 'library' : undefined,
      _partId: nextView.partId,
    }, searchPlaybackWorkspaceRef.current)) setAutoPlayFromSearch(false)
    setView(nextView)
    const nextHash = hashForView(nextView)
    if (window.location.hash === nextHash) return
    if (replace) window.history.replaceState(null, '', nextHash)
    else window.location.hash = nextHash
  }, [])

  const handleChangeSession = useCallback(() => {
    // Blocked during active jobs — all session-change affordances are
    // disabled so a successful download cannot disappear.
    if (jobIsActive) return
    sessionAbortControllerRef.current?.abort()
    sessionGenerationRef.current += 1
    stopPolling()
    stopSessionRefresh()
    setSelectedSession(null)
    setPlayerUrl(null)
    playerReadyRef.current = false
    setPreviewStale(false)
    setSubtitleStreams([])
    setSelectedSubtitle(-1)
    setSubtitleEntries([])
    setSubtitleSearch('')
    setSubtitleAnchor(-1)
    setSubtitleSelectionEnd(-1)
    setSubtitlesLoading(false)
    setStreamsLoading(false)
    setStreamsError(null)
    setSubtitlesError(null)
    setRenderState({status: null, error: null, downloadUrl: null, expiresAt: null, retryAfter: null, clipId: null, shareUrl: null})
    setJobSpec(null)
    jobIdRef.current = null
    setDownloadCompleted(false)
    setControlsChangedSinceJob(false)
    setStartPosition(null)
    setEndPosition(null)
    setAudioMode(AUDIO_MODES.STANDARD)
    setResolution(RENDER_RESOLUTIONS.NATIVE)
    setSubtitleOffsetMs(0)
    setTheaterMode(false)
    setAutoPlayFromSearch(false)
    setTrimFlashKey(0)
    navigateTo({name: 'home'})
  }, [jobIsActive, navigateTo, stopPolling, stopSessionRefresh])

  const handleSelectSession = useCallback((session) => {
    const workspaceView = workspaceViewForSession(session)
    if (!workspaceView) {
      console.warn('Rejected active session without a canonical workspace identity:', session)
      return
    }
    stopSessionRefresh()
    setSelectedSubtitle(-1)
    setSubtitleEntries([])
    setSubtitlesLoading(false)
    setSubtitlesError(null)
    setSelectedSession(session)
    navigateTo(workspaceView)
  }, [navigateTo, stopSessionRefresh])

// Library result selection — normalize the backend LibrarySearchResult into
// the session-shaped object the workspace expects, then route through the
// same selection path. The `_sourceType: 'library'` tag makes partId flow
// through streams/subtitles/preview/render only for library sources; active
// sessions are unchanged. Malformed results (missing/invalid ratingKey,
// mediaId, or partId) are rejected before workspace entry so the user never
// lands in a broken workspace that can't fetch streams or render.
const handleSelectLibraryResult = useCallback((result) => {
  const validationError = validateLibraryResult(result)
  if (validationError) {
    console.warn('Rejected malformed library result:', validationError, result)
    return
  }
  stopSessionRefresh()
  const session = libraryResultToSession(result)
  setSelectedSubtitle(-1)
  setSubtitleEntries([])
  setSubtitlesLoading(false)
  setSubtitlesError(null)
  setSelectedSession(session)
  navigateTo(workspaceViewForSession(session))
}, [navigateTo, stopSessionRefresh])

const handleSelectExternalSource = useCallback((result) => {
  const source = externalSourceToSession(result?.source || result)
  if (!source) {
    console.warn('Rejected malformed external source:', result)
    return
  }
  stopSessionRefresh()
  setSelectedSubtitle(-1)
  setSubtitleEntries([])
  setSubtitlesLoading(false)
  setSubtitlesError(null)
  setSelectedSession(source)
  navigateTo(workspaceViewForSession(source))
}, [navigateTo, stopSessionRefresh])

// Stable auth-required handler for the library search panel. setNeedsAuth is
// stable (useState setter), so this callback keeps a stable identity across
// session-poll re-renders — paired with the panel's ref capture it guarantees
// polling never re-fires a library search.
const handleLibraryAuthRequired = useCallback(() => setNeedsAuth(true), [])

  // ---------------------------------------------------------------- workspace deep-link hydration
  // A workspace URL is only a source identity. Trim, subtitle, audio, and job
  // state intentionally stay in React state rather than becoming shareable
  // URL state. Reuse an already-selected matching source when navigating back
  // to a workspace; otherwise hydrate it from the authoritative backend lane.
  const hydrationViewName = view.name
  const hydrationViewSourceId = view.sourceId
  const hydrationViewRatingKey = view.ratingKey
  const hydrationViewMediaId = view.mediaId
  const hydrationViewPartId = view.partId
  useEffect(() => {
    workspaceHydrationControllerRef.current?.abort()
    workspaceHydrationControllerRef.current = null
    const hydrationView = {
      name: hydrationViewName,
      sourceId: hydrationViewSourceId,
      ratingKey: hydrationViewRatingKey,
      mediaId: hydrationViewMediaId,
      partId: hydrationViewPartId,
    }

    if (hydrationView.name !== 'workspace') return undefined
    if (
      activeJobRef.current &&
      selectedSession &&
      !sessionMatchesWorkspace(selectedSession, hydrationView)
    ) {
      const canonicalView = workspaceViewForSession(selectedSession)
      if (canonicalView) navigateTo(canonicalView, true)
      return undefined
    }
    if (selectedSession && sessionMatchesWorkspace(selectedSession, hydrationView)) {
      stopSessionRefresh()
      return undefined
    }
    if (selectedSession && !sessionMatchesWorkspace(selectedSession, hydrationView)) {
      // Clear source-bound subtitle state before the hydrated source is
      // installed. This prevents the subtitle effect from combining the new
      // route with the previous session's selected track for one render.
      setSelectedSubtitle(-1)
      setSubtitleEntries([])
      setSubtitlesLoading(false)
      setSubtitlesError(null)
      setSelectedSession(null)
      return undefined
    }

    const controller = new AbortController()
    workspaceHydrationControllerRef.current = controller
    let current = true

    const failToHome = (error) => {
      if (!current || controller.signal.aborted || isAbortError(error)) return
      if (error?.code === 'auth') {
        setNeedsAuth(true)
        stopSessionRefresh()
      }
      navigateTo({name: 'home'}, true)
    }

    const hydrate = async () => {
      try {
        let session
        if (hydrationView.sourceId) {
          const response = await fetch(`/media-sources/${encodeURIComponent(hydrationView.sourceId)}`, {redirect: 'manual', signal: controller.signal})
          if (response.type === 'opaqueredirect' || response.status === 401 || response.status === 403) {
            throw authRequiredError()
          }
          if (!response.ok) throw new Error(`Could not load this external source (${response.status}).`)
          const result = await response.json()
          if (!result || String(result.sourceId) !== String(hydrationView.sourceId)) {
            throw new Error('The requested external source is no longer available.')
          }
          session = externalSourceToSession(result)
          if (!session) throw new Error('The requested external source is malformed.')
        } else if (hydrationView.partId == null) {
          const liveSessions = await fetchWorkspaceSessions(controller.signal)
          session = liveSessions.find(candidate => sessionMatchesWorkspace(candidate, hydrationView))
          if (!session) throw new Error('The active session is no longer available.')
        } else {
          const response = await fetch(
            `/library/source/${encodeURIComponent(hydrationView.ratingKey)}?mediaId=${hydrationView.mediaId}&partId=${hydrationView.partId}`,
            {redirect: 'manual', signal: controller.signal}
          )
          if (response.type === 'opaqueredirect' || response.status === 401 || response.status === 403) {
            throw authRequiredError()
          }
          if (!response.ok) throw new Error(`Could not load this library source (${response.status}).`)
          const result = await response.json()
          const validationError = validateLibraryResult(result)
          if (validationError) throw new Error(validationError)
          if (
            String(result.ratingKey) !== hydrationView.ratingKey ||
            Number(result.mediaId) !== hydrationView.mediaId ||
            Number(result.partId) !== hydrationView.partId
          ) {
            throw new Error('The requested library source is no longer available.')
          }
          session = libraryResultToSession(result)
        }

        if (!current || controller.signal.aborted) return
        stopSessionRefresh()
        setSelectedSubtitle(-1)
        setSubtitleEntries([])
        setSubtitlesLoading(false)
        setSubtitlesError(null)
        setSelectedSession(session)
      } catch (error) {
        failToHome(error)
      }
    }

    hydrate()
    return () => {
      current = false
      controller.abort()
      if (workspaceHydrationControllerRef.current === controller) {
        workspaceHydrationControllerRef.current = null
      }
    }
  }, [
    navigateTo, selectedSession, stopSessionRefresh,
    hydrationViewName, hydrationViewRatingKey, hydrationViewMediaId, hydrationViewPartId, hydrationViewSourceId,
  ])

  // ---------------------------------------------------------------- clip library navigation
  const openLibrary = useCallback(() => navigateTo({name: 'library'}), [navigateTo])
  const openSubtitleSearch = useCallback(() => navigateTo({name: 'subtitleSearch'}), [navigateTo])
  const openSubtitleResult = useCallback((hit) => {
    const mediaId = Number(hit?.mediaId)
    const partId = Number(hit?.partId)
    const startMs = Number(hit?.startMs)
    const endMs = Number(hit?.endMs)
    if (!hit?.ratingKey || !isPositiveSafeInteger(mediaId) || !isPositiveSafeInteger(partId) || !Number.isFinite(startMs) || !Number.isFinite(endMs) || startMs < 0 || endMs <= startMs) return
    const workspace = {name: 'workspace', ratingKey: String(hit.ratingKey), mediaId, partId}
    // A search hit must never switch a source while a render owns it. This is
    // the same protection used by hash navigation and source changes.
    if (jobIsActive && (!selectedSession || !sessionMatchesWorkspace(selectedSession, workspace))) return
    if (jobIsActive) return
    pendingSubtitleRangeRef.current = {workspace, startMs, endMs, subtitleIndex: Number(hit.subtitleIndex)}
    searchPlaybackWorkspaceRef.current = workspace
    setTheaterMode(true)
    navigateTo(workspace)
  }, [jobIsActive, navigateTo, selectedSession])
  const openClip = useCallback((clipId) => navigateTo({name: 'clip', clipId}), [navigateTo])
  const goHome = useCallback(() => navigateTo({name: 'home'}), [navigateTo])
  const goBackFromLibrary = useCallback(() => {
    const workspaceView = workspaceViewForSession(selectedSession)
    navigateTo(workspaceView || {name: 'home'})
  }, [navigateTo, selectedSession])
  const handleClipDeleted = useCallback(() => {
    // After deleting from the detail view, return to the library (which
    // re-fetches and reconciles).
    navigateTo({name: 'library'})
  }, [navigateTo])

  // ---------------------------------------------------------------- cleanup on unmount
  useEffect(() => {
    return () => {
      stopPolling()
      stopSessionRefresh()
    }
  }, [stopPolling, stopSessionRefresh])

  // ---------------------------------------------------------------- render
  const sessionsLoading = sessions === null && !sessionsError && !needsAuth
  const changeSessionDisabledReason = 'Finish or cancel the active render before changing sessions'
  const workspaceVisible = view.name === 'workspace'

  // Reference expiryTick so the countdown re-renders without affecting logic.
  void expiryTick

  return (
    <div className="cs-app">
      <Box className="cs-shell-bar" component="header">
        <Container maxWidth="lg">
          <Toolbar disableGutters sx={{gap: 2, minHeight: 64}}>
            <Box
              component="button"
              type="button"
              onClick={goHome}
              aria-label="CutScene home"
              sx={{
                display: 'flex', alignItems: 'baseline', gap: 0,
                background: 'none', border: 0, padding: 0, cursor: 'pointer',
                color: 'inherit', fontFamily: 'inherit',
                '&:focus-visible': {outline: '2px solid #ff7300', outlineOffset: 4, borderRadius: 2},
              }}
            >
              <Typography variant="h5" component="h1" sx={{fontWeight: 700, letterSpacing: '-0.02em'}}>
                Cut
              </Typography>
              <Typography variant="h5" component="span" className="cs-wordmark-accent" sx={{fontWeight: 700}}>
                Scene
              </Typography>
            </Box>
            <Box sx={{flex: 1}}/>
            {!needsAuth && (
              <Button
                variant={view.name === 'subtitleSearch' ? 'contained' : 'text'} color="primary" onClick={openSubtitleSearch}
                sx={view.name === 'subtitleSearch' ? {px: 2, py: 0.5} : {color: 'text.secondary', '&:hover': {color: '#ffd9b0'}}}
                aria-current={view.name === 'subtitleSearch' ? 'page' : undefined}
              >
                Subtitle search
              </Button>
            )}
            {!needsAuth && (
              <Button
                variant={view.name === 'library' || view.name === 'clip' ? 'contained' : 'text'}
                color="primary"
                onClick={openLibrary}
                sx={view.name === 'library' || view.name === 'clip'
                  ? {px: 2, py: 0.5}
                  : {color: 'text.secondary', '&:hover': {color: '#ffd9b0'}}}
                aria-current={view.name === 'library' || view.name === 'clip' ? 'page' : undefined}
              >
                Clips
              </Button>
            )}
          </Toolbar>
        </Container>
      </Box>

      <Box component="main" sx={{flex: 1, py: {xs: 2, md: 3}, position: 'relative', zIndex: 2}}>
        <Container className="cs-main-container" maxWidth={selectedSession && theaterMode && workspaceVisible ? 'xl' : 'lg'}>
          {needsAuth ? (
            <Stack spacing={2} alignItems="center" className="cs-rise" sx={{py: 5, textAlign: 'center'}}>
              <Typography variant="h4">Connect CutScene to Plex</Typography>
              <Typography variant="body2" sx={{color: 'text.secondary', maxWidth: 460}}>
                Sign in with Plex to see your active sessions and start clipping.
              </Typography>
              <Button variant="contained" color="primary" href="/authUrl" sx={{px: 3, py: 1}}>
                Log in with Plex
              </Button>
            </Stack>
          ) : view.name === 'subtitleSearch' ? (
            <SemanticSubtitleSearch onOpenResult={openSubtitleResult} onAuthRequired={handleLibraryAuthRequired}/>
          ) : view.name === 'library' ? (
            <ClipLibrary
              onOpenClip={openClip}
              onBack={goBackFromLibrary}
              hasActiveWorkspace={Boolean(selectedSession)}
            />
          ) : view.name === 'clip' ? (
            <ClipDetail
              clipId={view.clipId}
              onBack={openLibrary}
              onDeleted={handleClipDeleted}
            />
          ) : view.name !== 'workspace' || !selectedSession ? (
            <Stack spacing={4} className="cs-rise">
              <Box>
                <Typography variant="overline" className="cs-section-label" sx={{color: '#ff7300'}}>
                  Pick a source
                </Typography>
                <Typography variant="h4" sx={{mt: 0.5}}>Pick something to clip</Typography>
                <Typography variant="body2" sx={{color: 'text.secondary', mt: 0.5, maxWidth: 520}}>
                  Choose an active Plex session, or search your Plex library to pick a clip source.
                </Typography>
              </Box>
              <LibrarySearchPanel
                onSelect={handleSelectLibraryResult}
                onSelectExternalSource={handleSelectExternalSource}
                selectedKey={selectedSession?.ratingKey}
                onAuthRequired={handleLibraryAuthRequired}
                sessions={sessions}
                sessionsLoading={sessionsLoading}
                sessionsError={sessionsError}
                onSelectSession={handleSelectSession}
                onRetrySessions={retrySessions}
              />
            </Stack>
          ) : (
            <ClipWorkspace
              session={selectedSession}
              playerUrl={playerUrl}
              onPlayerError={() => setPlayerError(true)}
              onPlayerReady={() => setPlayerError(false)}
              previewStale={previewStale}
              onApplyPreview={applyPreview}
              startPosition={startPosition}
              endPosition={endPosition}
              onRangeChange={handleRangeChange}
              onStartChange={setBoundedStartPosition}
              onEndChange={setBoundedEndPosition}
              trimFlashKey={trimFlashKey}
              onChangeSession={handleChangeSession}
              changeSessionDisabled={jobIsActive}
              changeSessionDisabledReason={changeSessionDisabledReason}
              renderState={renderState}
              jobSpec={jobSpec}
              controlsChangedSinceJob={controlsChangedSinceJob}
              onCreateJob={() => createRenderJob()}
              onDownloadJob={handleDownloadJob}
              onRetryJob={retryRenderJob}
              onRetryPoll={retryPollJob}
              onCreateNewJob={() => createRenderJob()}
              onOpenClip={openClip}
              audioMode={audioMode}
              onAudioModeChange={setAudioMode}
              resolution={resolution}
              onResolutionChange={value => {
                setResolution(value)
                setControlsChangedSinceJob(true)
              }}
              theaterMode={theaterMode}
              onToggleTheater={() => setTheaterMode(mode => !mode)}
              autoPlay={autoPlayFromSearch}
              subtitleOffsetMs={subtitleOffsetMs}
              onSubtitleOffsetChange={setSubtitleOffsetMs}
              subtitle={selectedSubtitle}
              streams={subtitleStreams}
              selectedSubtitle={selectedSubtitle}
              onSubtitleChange={setSelectedSubtitle}
              streamsLoading={streamsLoading}
              streamsError={streamsError}
              listMode={subtitleSearch.trim() ? 'search' : 'full'}
              listEntries={subtitleSearch.trim() ? filteredSubtitleEntries : fullEntriesWithIdx}
              totalCount={subtitleEntries.length}
              anchor={subtitleAnchor}
              selectionRange={subtitleSelectionRange}
              onPick={handleSubtitlePick}
              search={subtitleSearch}
              onSearchChange={setSubtitleSearch}
              onClearSearch={() => setSubtitleSearch('')}
              loading={subtitlesLoading}
              error={subtitlesError}
              listRef={subtitleListRef}
            />
          )}
        </Container>
      </Box>

      {playerError && selectedSession && workspaceVisible && (
        <Container maxWidth="lg" sx={{pb: 2, position: 'relative', zIndex: 2}}>
          <Typography variant="caption" sx={{color: '#ffb4a8'}} role="alert">
            Preview failed to load. Adjust the trim or change the session to retry.
          </Typography>
        </Container>
      )}

      {downloadCompleted && workspaceVisible && (
        <Container maxWidth="lg" sx={{pb: 2, position: 'relative', zIndex: 2}}>
          <Typography variant="caption" sx={{color: '#7fd391'}} role="status">
            Download started — check your browser downloads.
          </Typography>
        </Container>
      )}
    </div>
  )
}

export default App
