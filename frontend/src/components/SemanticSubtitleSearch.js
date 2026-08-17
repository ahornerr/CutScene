import {Box, Button, Chip, CircularProgress, Dialog, DialogActions, DialogContent, DialogTitle, LinearProgress, Paper, Stack, TextField, Typography} from '@mui/material'
import {useEffect, useRef, useState} from 'react'
import {isAbortError, millisToDuration} from '../utils'
import {stripSubtitleMarkup} from './subtitle-markup'

function contextFor(hit) {
  const episode = hit.season != null && hit.episode != null ? `S${hit.season} · E${hit.episode}` : ''
  return [hit.showTitle, episode, hit.year].filter(Boolean).join('  ·  ')
}

function rangeFor(hit) {
  return `${millisToDuration(hit.startMs)} – ${millisToDuration(hit.endMs)}`
}

function normalizedExcerpt(text) {
  // Keep this deliberately display-only: remove any remaining angle-bracket
  // construct after the shared subtitle normalizer, then let React escape the
  // resulting string. No backend text ever becomes markup in this view.
  return stripSubtitleMarkup(text || '').replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim()
}

// Job payloads have evolved independently of the search UI. Read common
// camelCase/snake_case variants from either the job or its progress object so
// resumed jobs remain understandable through compatible backend versions.
function jobField(job, names, fallback = undefined) {
  const sources = [job?.progress, job]
  for (const source of sources) {
    if (!source) continue
    for (const name of names) {
      if (source[name] != null) return source[name]
    }
  }
  return fallback
}

function jobCount(job, names) {
  const value = Number(jobField(job, names, 0))
  return Number.isFinite(value) ? value : 0
}

function hasJobField(job, names) {
  return names.some(name => job?.progress?.[name] != null || job?.[name] != null)
}

export default function SemanticSubtitleSearch({onOpenResult, onAuthRequired}) {
  const [query, setQuery] = useState('')
  const [submittedQuery, setSubmittedQuery] = useState('')
  const [results, setResults] = useState(null)
  const [error, setError] = useState(null)
  const controllerRef = useRef(null)
  const indexControllerRef = useRef(null)
  const indexPollRef = useRef(null)
  const [indexConfirmOpen, setIndexConfirmOpen] = useState(false)
  const [indexJob, setIndexJob] = useState(null)
  const [indexError, setIndexError] = useState(null)
  const [indexAccess, setIndexAccess] = useState('unknown')

  useEffect(() => () => {
    controllerRef.current?.abort()
    indexControllerRef.current?.abort()
    if (indexPollRef.current) clearTimeout(indexPollRef.current)
  }, [])

  const handleAuthResponse = response => {
    if (response.type === 'opaqueredirect' || response.status === 401 || response.status === 403) {
      onAuthRequired?.()
      return true
    }
    return false
  }

  const isTerminalIndexJob = job => ['completed', 'complete', 'succeeded', 'success', 'failed', 'error', 'cancelled', 'interrupted'].includes(String(job?.state || job?.status || '').toLowerCase())

  const pollIndexJob = async (id, controller) => {
    try {
      const response = await fetch(`/subtitle-search/index-jobs/${encodeURIComponent(id)}`, {signal: controller.signal, redirect: 'manual'})
      if (handleAuthResponse(response)) return
      if (!response.ok) throw new Error('Couldn’t check indexing progress.')
      const job = await response.json()
      if (controller.signal.aborted) return
      setIndexJob(job)
      if (!isTerminalIndexJob(job)) indexPollRef.current = setTimeout(() => pollIndexJob(id, controller), 1500)
    } catch (nextError) {
      if (!controller.signal.aborted && !isAbortError(nextError)) setIndexError(nextError.message || 'Couldn’t check indexing progress.')
    }
  }

  // The server keeps the most recent active job, so returning to this page can
  // reconnect to it without briefly replacing the panel with a new-job state.
  const restoreCurrentIndexJob = async (controller) => {
    try {
      const response = await fetch('/subtitle-search/index-jobs/current', {signal: controller.signal, redirect: 'manual'})
      // Indexing is owner-only. A 403 here is an intentional capability limit,
      // unlike search/session auth failures, and must not send viewers to login.
      if (response.status === 403) {
        if (!controller.signal.aborted) setIndexAccess('forbidden')
        return null
      }
      if (handleAuthResponse(response) || response.status === 404) return null
      if (!response.ok) throw new Error('Couldn’t restore indexing progress.')
      const job = await response.json()
      // A valid current-job response is an object with an identity. This also
      // safely ignores older/invalid endpoint bodies without changing the UI.
      if (controller.signal.aborted || !job || Array.isArray(job) || !job.id) return null
      setIndexAccess('allowed')
      setIndexJob(job)
      setIndexError(null)
      if (!isTerminalIndexJob(job)) indexPollRef.current = setTimeout(() => pollIndexJob(job.id, controller), 1500)
      return job
    } catch (nextError) {
      if (!controller.signal.aborted && !isAbortError(nextError)) setIndexError(nextError.message || 'Couldn’t restore indexing progress.')
      return null
    }
  }

  useEffect(() => {
    const controller = new AbortController()
    restoreCurrentIndexJob(controller)
    return () => controller.abort()
  // Current status is intentionally restored once per page visit.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const startIndexing = async () => {
    setIndexConfirmOpen(false)
    indexControllerRef.current?.abort()
    const controller = new AbortController()
    indexControllerRef.current = controller
    setIndexJob({state: 'starting'})
    setIndexError(null)
    try {
      const response = await fetch('/subtitle-search/index-all', {method: 'POST', signal: controller.signal, redirect: 'manual'})
      if (response.status === 403) {
        setIndexJob(null)
        setIndexAccess('forbidden')
        return
      }
      if (handleAuthResponse(response)) return
      if (response.status === 409) {
        // Another request owns an active job. Restore its authoritative status
        // instead of leaving a transient "starting" state or generic error.
        const current = await restoreCurrentIndexJob(controller)
        if (!current && !controller.signal.aborted) {
          setIndexJob(null)
          setIndexError('An indexing job is already in progress.')
        }
        return
      }
      if (!response.ok) {
        const body = await response.json().catch(() => null)
        throw new Error(body?.error?.message || 'Couldn’t start subtitle indexing.')
      }
      const job = await response.json()
      if (controller.signal.aborted) return
      setIndexAccess('allowed')
      setIndexJob(job)
      if (!isTerminalIndexJob(job)) indexPollRef.current = setTimeout(() => pollIndexJob(job.id, controller), 1500)
    } catch (nextError) {
      if (!controller.signal.aborted && !isAbortError(nextError)) {
        setIndexJob(null)
        setIndexError(nextError.message || 'Couldn’t start subtitle indexing.')
      }
    }
  }

  const search = async (event) => {
    event.preventDefault()
    const trimmed = query.trim()
    if (!trimmed) return
    controllerRef.current?.abort()
    const controller = new AbortController()
    controllerRef.current = controller
    setSubmittedQuery(trimmed)
    setResults(null)
    setError(null)
    try {
      const response = await fetch(`/subtitle-search?q=${encodeURIComponent(trimmed)}`, {signal: controller.signal, redirect: 'manual'})
      if (handleAuthResponse(response)) return
      if (!response.ok) {
        const body = await response.json().catch(() => null)
        throw new Error(body?.error?.message || 'Couldn’t search subtitles right now.')
      }
      const data = await response.json()
      if (!controller.signal.aborted) setResults(Array.isArray(data) ? data : [])
    } catch (nextError) {
      if (!controller.signal.aborted && !isAbortError(nextError)) setError(nextError.message || 'Couldn’t search subtitles right now.')
    }
  }

  const clear = () => {
    controllerRef.current?.abort()
    setQuery('')
    setSubmittedQuery('')
    setResults(null)
    setError(null)
  }

  const loading = Boolean(submittedQuery && results === null && !error)
  const indexState = String(indexJob?.state || indexJob?.status || '').toLowerCase()
  const indexActive = Boolean(indexJob && !isTerminalIndexJob(indexJob))
  const indexFailed = ['failed', 'error', 'cancelled'].includes(indexState)
  const discovered = jobCount(indexJob, ['discovered', 'total', 'tracksDiscovered', 'tracks_discovered', 'sourcesDiscovered', 'sources_discovered', 'itemsDiscovered', 'items_discovered'])
  const processed = jobCount(indexJob, ['processed', 'scanned', 'tracksProcessed', 'tracks_processed', 'processedSources', 'processed_sources', 'processedItems', 'processed_items'])
  const indexedChunks = jobCount(indexJob, ['indexedChunks', 'indexed_chunks'])
  const hasIndexedChunks = hasJobField(indexJob, ['indexedChunks', 'indexed_chunks'])
  const indexed = hasIndexedChunks ? indexedChunks : jobCount(indexJob, ['indexed', 'tracksIndexed', 'tracks_indexed', 'indexedSources', 'indexed_sources', 'indexedItems', 'indexed_items'])
  const unchangedNames = ['unchangedTracks', 'unchanged_tracks', 'skippedUnchanged', 'skipped_unchanged', 'unchangedSkipped', 'unchanged_skipped', 'alreadyIndexed', 'already_indexed', 'alreadyIndexedSources', 'already_indexed_sources', 'unchangedSources', 'unchanged_sources']
  const unsupportedNames = ['unsupportedTracks', 'unsupported_tracks', 'unsupported', 'unsupportedSources', 'unsupported_sources', 'skippedUnsupported', 'skipped_unsupported', 'nonEnglish', 'non_english', 'external', 'externalSources', 'external_sources', 'image', 'imageSources', 'image_sources']
  const emptyNames = ['emptyTracks', 'empty_tracks', 'empty', 'emptySources', 'empty_sources', 'unextractable', 'unextractableSources', 'unextractable_sources', 'skippedEmpty', 'skipped_empty', 'noSubtitleTracks', 'no_subtitle_tracks']
  const skippedUnchanged = jobCount(indexJob, unchangedNames)
  const unsupported = jobCount(indexJob, unsupportedNames)
  const empty = jobCount(indexJob, emptyNames)
  const noSubtitleSkips = jobCount(indexJob, ['skippedNoSubtitles', 'skipped_no_subtitles', 'noSubtitleSkipped', 'no_subtitle_skipped'])
  const skipped = jobCount(indexJob, ['skipped', 'tracksSkipped', 'tracks_skipped'])
  const failed = jobCount(indexJob, ['failed', 'failures', 'errors', 'tracksFailed', 'tracks_failed', 'failedSources', 'failed_sources'])
  const sourcesNamed = hasJobField(indexJob, ['sourcesDiscovered', 'sources_discovered', 'processedSources', 'processed_sources', 'indexedSources', 'indexed_sources', 'alreadyIndexedSources', 'already_indexed_sources', 'unchangedSources', 'unchanged_sources', 'unsupportedSources', 'unsupported_sources', 'externalSources', 'external_sources', 'imageSources', 'image_sources', 'emptySources', 'empty_sources', 'unextractableSources', 'unextractable_sources', 'failedSources', 'failed_sources'])
  const unitLabel = sourcesNamed ? 'sources' : 'tracks'
  const unchangedLabel = hasJobField(indexJob, ['unchangedTracks', 'unchanged_tracks']) ? 'Unchanged tracks' : 'Already indexed'
  const unsupportedLabel = hasJobField(indexJob, ['unsupportedTracks', 'unsupported_tracks']) ? 'Unsupported tracks' : 'Unsupported'
  const emptyLabel = hasJobField(indexJob, ['emptyTracks', 'empty_tracks']) ? 'Empty tracks' : 'Empty'
  const resumed = Boolean(jobField(indexJob, ['resumed', 'wasResumed', 'was_resumed', 'isResumed', 'is_resumed'])) || indexState === 'resuming'
  const currentTitle = jobField(indexJob, ['currentTitle', 'current_title', 'currentItemTitle', 'current_item_title'])
  const progressTotal = discovered
  const progressDone = processed
  const progressValue = progressTotal > 0 ? Math.min(100, (progressDone / progressTotal) * 100) : undefined
  return <Stack spacing={3} className="cs-subtitle-search">
    <Box>
      <Typography variant="overline" className="cs-section-label" sx={{color: '#ff7300'}}>Subtitle search</Typography>
      <Typography variant="h4" sx={{mt: .5}}>Find a line, not just a title</Typography>
      <Typography variant="body2" sx={{color: 'text.secondary', mt: .75, maxWidth: 580}}>
        Search across your subtitle library by meaning. Open a match to start a clip around that moment.
      </Typography>
    </Box>
    <Paper variant="outlined" sx={{p: {xs: 1.5, sm: 2}, backgroundColor: 'rgba(255,115,0,.035)', borderColor: 'rgba(255,255,255,.1)'}}>
      <Stack direction={{xs: 'column', sm: 'row'}} spacing={1.5} justifyContent="space-between" alignItems={{sm: 'center'}}>
        <Box><Typography variant="subtitle2">Index your library</Typography><Typography variant="body2" color="text.secondary">Make every subtitle available for semantic search.</Typography></Box>
        {indexAccess === 'forbidden' ? <Typography variant="body2" color="text.secondary">Library indexing is available to the server owner.</Typography> : <Button variant="outlined" onClick={() => setIndexConfirmOpen(true)} disabled={indexActive}>Index library</Button>}
      </Stack>
      {(indexActive || (indexJob && isTerminalIndexJob(indexJob)) || indexError) && <Box aria-live="polite" sx={{mt: 1.75}}>
        {indexActive && <><LinearProgress variant={progressValue == null ? 'indeterminate' : 'determinate'} value={progressValue} sx={{mb: 1}}/><Typography variant="body2">{indexState === 'starting' ? 'Starting library index…' : indexState === 'resuming' ? 'Resuming library index…' : `Indexing${currentTitle ? `: ${currentTitle}` : '…'}`}</Typography>{resumed && <Typography variant="caption" color="text.secondary">This scan checks the library again and skips tracks that are already indexed and unchanged.</Typography>}</>}
        {indexJob && (indexActive || isTerminalIndexJob(indexJob)) && <Typography variant="caption" color="text.secondary">Discovered {discovered} · Processed {processed} · {hasIndexedChunks ? 'Indexed chunks' : 'Indexed'} {indexed}{skippedUnchanged ? ` · ${unchangedLabel} ${skippedUnchanged}` : ''}{unsupported ? ` · ${unsupportedLabel} ${unsupported}` : ''}{empty ? ` · ${emptyLabel} ${empty}` : ''}{noSubtitleSkips ? ` · No subtitles ${noSubtitleSkips}` : ''}{!skippedUnchanged && !unsupported && !empty && !noSubtitleSkips ? ` · Skipped ${skipped}` : ''} · Failed {failed} {unitLabel}</Typography>}
        {indexJob && !indexActive && !indexFailed && <Typography variant="body2" color="success.light">Library indexing is complete.</Typography>}
        {indexFailed && <Typography variant="body2" color="error.light">{indexState === 'interrupted' ? 'Indexing was interrupted. You can start it again to resume scanning.' : 'Indexing didn’t finish. You can try again.'}</Typography>}
        {indexError && <Typography variant="body2" color="error.light">{indexError}</Typography>}
      </Box>}
    </Paper>
    <Box component="form" onSubmit={search} sx={{display: 'flex', gap: 1, alignItems: 'flex-start'}}>
      <TextField fullWidth autoFocus label="Search subtitles" placeholder="Try “I need a plan”" value={query}
        onChange={event => setQuery(event.target.value)} inputProps={{'aria-label': 'Search subtitles'}} />
      <Button type="submit" variant="contained" disabled={!query.trim() || loading} sx={{height: 56, px: {xs: 2, sm: 3}}}>Search</Button>
    </Box>
    {(submittedQuery || error) && <Box aria-live="polite" aria-busy={loading || undefined}>
      {loading && <Stack direction="row" spacing={1.5} alignItems="center" sx={{py: 4, color: 'text.secondary'}}><CircularProgress size={20}/><Typography>Searching subtitle moments…</Typography></Stack>}
      {error && <Paper variant="outlined" sx={{p: 2, borderColor: 'rgba(255,130,110,.45)'}}><Typography color="error.light">{error}</Typography><Button size="small" onClick={search} sx={{mt: 1}}>Try again</Button></Paper>}
      {results && results.length === 0 && <Paper variant="outlined" sx={{p: 3, textAlign: 'center'}}><Typography>No subtitle moments found for “{submittedQuery}”.</Typography><Typography variant="body2" color="text.secondary" sx={{mt: .5}}>Try a shorter phrase or describe the idea differently.</Typography></Paper>}
      {results?.length > 0 && <Stack spacing={1.25}>
        <Typography variant="body2" color="text.secondary">{results.length} {results.length === 1 ? 'match' : 'matches'} for “{submittedQuery}”</Typography>
        {results.map((hit, index) => <Paper key={`${hit.ratingKey}-${hit.mediaId}-${hit.partId}-${hit.startMs}-${index}`} variant="outlined" className="cs-subtitle-hit">
          <Box sx={{p: {xs: 2, sm: 2.5}}}>
            <Stack direction="row" justifyContent="space-between" spacing={2} alignItems="flex-start">
              <Box sx={{minWidth: 0}}><Typography variant="h6">{hit.title}</Typography><Typography variant="body2" color="text.secondary">{contextFor(hit)}</Typography></Box>
              <Chip label={rangeFor(hit)} size="small" />
            </Stack>
            {/* Search excerpts are always plain normalized subtitle text. This
                deliberately strips subtitle-like tags rather than interpreting
                backend content, so malformed markup and hostile HTML are inert. */}
            <Typography className="cs-subtitle-quote" sx={{mt: 2}}>“{normalizedExcerpt(hit.text)}”</Typography>
            <Button onClick={() => onOpenResult(hit)} variant="text" sx={{mt: 1.5, ml: -.75}}>Open in workspace →</Button>
          </Box>
        </Paper>)}
      </Stack>}
    </Box>}
    {submittedQuery && <Button onClick={clear} variant="text" sx={{alignSelf: 'flex-start', color: 'text.secondary'}}>Clear search</Button>}
    <Dialog open={indexConfirmOpen} onClose={() => setIndexConfirmOpen(false)} aria-labelledby="index-library-title">
      <DialogTitle id="index-library-title">Index the whole library?</DialogTitle>
      <DialogContent><Typography>CutScene will scan subtitles across your library. This can take a while, and you can keep using search while it runs.</Typography></DialogContent>
      <DialogActions><Button onClick={() => setIndexConfirmOpen(false)}>Cancel</Button><Button variant="contained" onClick={startIndexing}>Start indexing</Button></DialogActions>
    </Dialog>
  </Stack>
}
