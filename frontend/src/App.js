import './App.css';
import {useCallback, useEffect, useMemo, useRef, useState} from "react";
import {
  Box, Button, Container, Stack, Toolbar, Typography,
} from "@mui/material";
import SessionPicker from "./components/SessionPicker";
import ClipWorkspace from "./components/ClipWorkspace";
import {stripSubtitleMarkup} from "./components/subtitle-markup";
import {
  buildRenderJobRequest, buildRenderJobRequestFromSpec, isTerminal, isActive, JOB_STATES, snapshotJobSpec, AUDIO_MODES,
} from "./components/render-jobs";
import {
  isAbortError, isPgsSubtitleStream, millisToDuration, MIN_GAP_MS,
} from "./utils";

const POLL_INTERVAL_MS = 2000
const MAX_NETWORK_RETRIES = 5
const EXPIRY_TICK_MS = 1000

function App() {
  // --- Session list ---
  const [sessions, setSessions] = useState(null)
  const [sessionsError, setSessionsError] = useState(null)
  const [needsAuth, setNeedsAuth] = useState(false)

  // --- Active clip state ---
  const [selectedSession, setSelectedSession] = useState(null)
  const [startPosition, setStartPosition] = useState(null)
  const [endPosition, setEndPosition] = useState(null)
  const [playerUrl, setPlayerUrl] = useState(null)
  const [playerError, setPlayerError] = useState(false)
  const [previewStale, setPreviewStale] = useState(false)
  const [audioMode, setAudioMode] = useState(AUDIO_MODES.STANDARD)

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
  const [renderState, setRenderState] = useState({status: null, error: null, downloadUrl: null, expiresAt: null, retryAfter: null})
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
  const pollControllerRef = useRef(null)
  const pollTimeoutRef = useRef(null)
  const networkRetryCountRef = useRef(0)
  const jobIdRef = useRef(null)

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
    setRenderState({status: null, error: null, downloadUrl: null, expiresAt: null, retryAfter: null})
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
  useEffect(() => {
    let active = true
    fetch('/sessions', {redirect: "manual"})
      .then(response => {
        if (response.type === "opaqueredirect") {
          setNeedsAuth(true)
          return null
        }
        if (!response.ok) {
          throw new Error(`Error fetching sessions: ${response.status} ${response.statusText}`)
        }
        return response.json()
      })
      .then(json => { if (active) setSessions(json || []) })
      .catch(err => {
        if (!active) return
        console.error('Error fetching sessions:', err)
        setSessionsError(err)
      })
    return () => { active = false }
  }, [])

  // ---------------------------------------------------------------- player URL builder
  const setPlayerPosition = useCallback((start, end, subtitleOverride = selectedSubtitle, audioModeOverride = audioMode) => {
    const subtitleIndex = subtitleOverride
    const subtitleParam = subtitleIndex >= 0 ? `&subtitle=${subtitleIndex}` : ''
    const audioModeParam = audioModeOverride && audioModeOverride !== AUDIO_MODES.STANDARD
      ? `&audioMode=${audioModeOverride}`
      : ''
    setPlayerError(false)
    setPlayerUrl(
      `/preview/${selectedSession.ratingKey}/${millisToDuration(start)}/${millisToDuration(end)}` +
      `?mediaId=${selectedSession.Media[0].Part[0].id}${subtitleParam}${audioModeParam}`
    )
  }, [selectedSession, selectedSubtitle, audioMode])

  // ---------------------------------------------------------------- session change → streams + prewarm
  useEffect(() => {
    const generation = sessionGenerationRef.current + 1
    sessionGenerationRef.current = generation
    sessionAbortControllerRef.current?.abort()

    const controller = new AbortController()
    sessionAbortControllerRef.current = controller
    playerReadyRef.current = false

    if (selectedSession) {
      const [initStart, initEnd] = clampToBounds(selectedSession, selectedSession.viewOffset, selectedSession.viewOffset + 60000)
      const mediaId = selectedSession.Media[0].Part[0].id
      setPlayerError(false)
      setPreviewStale(false)

      setStartPosition(initStart)
      setEndPosition(initEnd)
      setAudioMode(AUDIO_MODES.STANDARD)
      setSubtitleStreams([])
      setSelectedSubtitle(-1)
      setStreamsError(null)
      setSubtitlesError(null)
      setStreamsLoading(true)

      resetRenderState()

      fetch(`/streams/${selectedSession.ratingKey}?mediaId=${encodeURIComponent(mediaId)}`, {signal: controller.signal})
        .then(r => r.json())
        .then(streams => {
          if (controller.signal.aborted || sessionGenerationRef.current !== generation) return
          const availableStreams = Array.isArray(streams) ? streams : []
          const selectedStreamIndex = availableStreams.length > 0 ? availableStreams[0].index : -1
          setSubtitleStreams(availableStreams)
          setStreamsLoading(false)
          playerReadyRef.current = true
          if (selectedStreamIndex >= 0) {
            setSelectedSubtitle(selectedStreamIndex)
          }
          const unselectedTextStreams = availableStreams.filter(stream =>
            stream.type === 'text' &&
            !isPgsSubtitleStream(stream) &&
            stream.index !== selectedStreamIndex
          )
          unselectedTextStreams.forEach(stream => {
            fetch(`/subtitles/${selectedSession.ratingKey}?subtitle=${stream.index}&mediaId=${mediaId}`, {
              signal: controller.signal,
            })
              .then(r => r.json())
              .catch(() => {})
          })
        })
        .catch(err => {
          if (!controller.signal.aborted && !isAbortError(err) && sessionGenerationRef.current === generation) {
            console.log('Could not fetch subtitle streams:', err)
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
    fetch(`/subtitles/${selectedSession.ratingKey}?subtitle=${selectedSubtitle}&mediaId=${mediaId}`, {
      signal: controller.signal,
    })
      .then(r => r.json())
      .then(entries => {
        if (controller.signal.aborted || sessionGenerationRef.current !== generation) return
        setSubtitleEntries(entries || [])
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
    setPlayerPosition(startPosition, endPosition)
    setPreviewStale(false)
    setControlsChangedSinceJob(true)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedSubtitle, streamsLoading])

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

  // ---------------------------------------------------------------- bound setters (clamp to duration only)
  // A typed timestamp commit is a single discrete, intentional action — like
  // picking a subtitle or changing audio mode — so it auto-applies the preview
  // exactly once. The slider drag stays deferred behind the stale button
  // because it's continuous. BoundField.commit only calls onTimeChange for a
  // valid parse + gap, so intermediate invalid text never reaches here.
  const setBoundedStartPosition = useCallback((newValue) => {
    const duration = selectedSession?.duration ?? Infinity
    const clamped = Math.max(0, Math.min(newValue, duration))
    if (clamped < endPosition - MIN_GAP_MS) {
      setStartPosition(clamped)
      setPlayerPosition(clamped, endPosition)
      setPreviewStale(false)
      setControlsChangedSinceJob(true)
    }
  }, [endPosition, selectedSession, setPlayerPosition])

  const setBoundedEndPosition = useCallback((newValue) => {
    const duration = selectedSession?.duration ?? Infinity
    const clamped = Math.max(0, Math.min(newValue, duration))
    if (clamped > startPosition + MIN_GAP_MS) {
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
    setStartPosition(cs)
    setEndPosition(ce)
    setPreviewStale(true)
    setControlsChangedSinceJob(true)
  }, [selectedSession])

  // ---------------------------------------------------------------- subtitle pick (clamp to duration)
  const handleSubtitlePick = useCallback((fullIdx, shiftKey) => {
    const duration = selectedSession?.duration ?? Infinity
    if (subtitleSearch.trim()) {
      const entry = subtitleEntries[fullIdx]
      if (!entry) return
      const newStart = Math.max(0, Math.min(entry.start - 500, duration))
      const newEnd = Math.max(0, Math.min(entry.end + 500, duration))
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
    const newStart = Math.max(0, Math.min(firstEntry.start - 500, duration))
    const newEnd = Math.max(0, Math.min(lastEntry.end + 500, duration))
    setStartPosition(newStart)
    setEndPosition(newEnd)
    setPreviewStale(false)
    setPlayerPosition(newStart, newEnd)
    flashTrim()
    setControlsChangedSinceJob(true)
  }, [subtitleSearch, subtitleAnchor, subtitleEntries, setPlayerPosition, flashTrim, selectedSession])

  // ---------------------------------------------------------------- render-job create
  const createRenderJob = useCallback((retrySpec = null) => {
    if (!selectedSession || startPosition == null || endPosition == null) return
    stopPolling()
    setDownloadCompleted(false)
    networkRetryCountRef.current = 0

    const spec = retrySpec || snapshotJobSpec(selectedSession, startPosition, endPosition, selectedSubtitle, subtitleStreams, audioMode)
    let body
    try {
      body = JSON.stringify(retrySpec
        ? buildRenderJobRequestFromSpec(retrySpec)
        : buildRenderJobRequest(selectedSession, startPosition, endPosition, selectedSubtitle, audioMode))
    } catch (error) {
      setRenderState({
        status: JOB_STATES.FAILED,
        error: {code: 'validation_error', message: error?.message || 'The selected media part has an invalid media ID.', retryable: false},
        downloadUrl: null, expiresAt: null, retryAfter: null,
      })
      return
    }

    setJobSpec(spec)
    setControlsChangedSinceJob(false)
    setRenderState({status: JOB_STATES.QUEUED, error: null, downloadUrl: null, expiresAt: null, retryAfter: null})

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
        })
        pollForJobRef.current(job.id, controller)
      })
      .catch(err => {
        if (controller.signal.aborted || isAbortError(err)) return
        setRenderState({
          status: JOB_STATES.FAILED,
          error: {code: 'request_error', message: err?.message || 'Could not start rendering.', retryable: err?.retryable || false},
          downloadUrl: null, expiresAt: null, retryAfter: err?.retryAfter || null,
        })
      })
  }, [selectedSession, startPosition, endPosition, selectedSubtitle, subtitleStreams, audioMode, stopPolling])

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
            })
            if (!isTerminal(job.status)) {
              pollTimeoutRef.current = setTimeout(poll, POLL_INTERVAL_MS)
            }
          })
          .catch(err => {
            if (controller.signal.aborted || isAbortError(err)) return
            if (err?.message === '__gone__') {
              setRenderState({status: JOB_STATES.EXPIRED, error: null, downloadUrl: null, expiresAt: null, retryAfter: null})
              return
            }
            if (err?.message === '__auth__') {
              setRenderState({
                status: JOB_STATES.FAILED,
                error: {code: 'auth_error', message: 'Authentication expired. Reload the page and sign in again.', retryable: false},
                downloadUrl: null, expiresAt: null, retryAfter: null,
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
                  downloadUrl: null, expiresAt: null, retryAfter: null,
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
                downloadUrl: null, expiresAt: null, retryAfter: null,
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
        // Transition to expired — clear the download URL so it can't be used.
        setRenderState(prev => ({
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

  const handleChangeSession = useCallback(() => {
    // Blocked during active jobs — all session-change affordances are
    // disabled so a successful download cannot disappear.
    if (jobIsActive) return
    sessionAbortControllerRef.current?.abort()
    sessionGenerationRef.current += 1
    stopPolling()
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
    setRenderState({status: null, error: null, downloadUrl: null, expiresAt: null, retryAfter: null})
    setJobSpec(null)
    jobIdRef.current = null
    setDownloadCompleted(false)
    setControlsChangedSinceJob(false)
    setStartPosition(null)
    setEndPosition(null)
    setAudioMode(AUDIO_MODES.STANDARD)
    setTrimFlashKey(0)
  }, [jobIsActive, stopPolling])

  // ---------------------------------------------------------------- cleanup on unmount
  useEffect(() => {
    return () => stopPolling()
  }, [stopPolling])

  // ---------------------------------------------------------------- render
  const sessionsLoading = sessions === null && !sessionsError && !needsAuth
  const changeSessionDisabledReason = 'Finish or cancel the active render before changing sessions'

  // Reference expiryTick so the countdown re-renders without affecting logic.
  void expiryTick

  return (
    <div className="cs-app">
      <Box className="cs-shell-bar" component="header">
        <Container maxWidth="lg">
          <Toolbar disableGutters sx={{gap: 2, minHeight: 64}}>
            <Box sx={{display: 'flex', alignItems: 'baseline', gap: 0}}>
              <Typography variant="h5" component="h1" sx={{fontWeight: 700, letterSpacing: '-0.02em'}}>
                Cut
              </Typography>
              <Typography variant="h5" component="span" className="cs-wordmark-accent" sx={{fontWeight: 700}}>
                Scene
              </Typography>
            </Box>
            <Box sx={{flex: 1}}/>
            {selectedSession && (
              <Button
                variant="outlined"
                size="small"
                onClick={handleChangeSession}
                disabled={jobIsActive}
                title={jobIsActive ? changeSessionDisabledReason : ''}
                sx={{borderColor: 'rgba(255,255,255,0.2)'}}
              >
                Change session
              </Button>
            )}
          </Toolbar>
        </Container>
      </Box>

      <Box component="main" sx={{flex: 1, py: {xs: 2, md: 3}, position: 'relative', zIndex: 2}}>
        <Container maxWidth="lg">
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
          ) : !selectedSession ? (
            <Stack spacing={2.5} className="cs-rise">
              <Box>
                <Typography variant="overline" className="cs-section-label" sx={{color: '#ff7300'}}>
                  Active sessions
                </Typography>
                <Typography variant="h4" sx={{mt: 0.5}}>Pick something to clip</Typography>
                <Typography variant="body2" sx={{color: 'text.secondary', mt: 0.5, maxWidth: 520}}>
                  Choose an active Plex session to open the clip workspace — preview, trim, and grab a clip with subtitles.
                </Typography>
              </Box>
              <SessionPicker
                sessions={sessions}
                loading={sessionsLoading}
                error={sessionsError}
                selectedKey={selectedSession?.ratingKey}
                onSelect={setSelectedSession}
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
              audioMode={audioMode}
              onAudioModeChange={setAudioMode}
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

      {playerError && selectedSession && (
        <Container maxWidth="lg" sx={{pb: 2, position: 'relative', zIndex: 2}}>
          <Typography variant="caption" sx={{color: '#ffb4a8'}} role="alert">
            Preview failed to load. Adjust the trim or change the session to retry.
          </Typography>
        </Container>
      )}

      {downloadCompleted && (
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
