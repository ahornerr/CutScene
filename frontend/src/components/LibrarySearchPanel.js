import {Box, Button, Grid, IconButton, InputAdornment, LinearProgress, TextField, Typography} from "@mui/material";
import {useCallback, useEffect, useRef, useState} from "react";
import LibraryResultCard from "./LibraryResultCard";
import SessionCard from "./SessionCard";
import {SessionEmptyState, SessionErrorState, SessionLoadingGrid, orderSessionsOwnedFirst} from "./SessionPicker";
import StateMessage from "./StateMessage";
import {isAbortError} from "../utils";

// Unified Plex source picker. One heading, one search field, one results
// region. With an empty query the region shows active Plex sessions as the
// immediate/default list; typing a valid query swaps that region to library
// matches — never a separate competing section. Clearing the query restores
// the session list.
//
// Source-type identity is preserved through badges: active-session cards carry
// the warm "Your session" badge, library cards carry the cooler "Library"
// badge. Both render in the same Grid so they read as one cohesive list.
//
// Behaviour:
//   • 300ms debounce + a 2-character minimum before firing a library search
//   • the backend caps queries at 128 runes; we enforce the same bound
//     client-side so a long paste never produces a 422 round-trip
//   • every raw input edit immediately aborts the in-flight request and bumps
//     a generation token, so a slow response for an old query can never
//     commit (the debounce timer alone is not enough — a request fired for a
//     prior committed query could resolve during the new debounce window)
//   • 401/403 route to onAuthRequired so the app shows its login state
//   • 422 is validation feedback (non-retryable); retry is offered only for
//     network failures, 429, and 5xx
//   • one coherent status region (aria-live=polite) carries the loading /
//     error / empty / results announcements without duplication
//   • active-session loading/error/empty states render in the same region,
//     so the picker never shows a busy two-panel layout

const DEBOUNCE_MS = 300
const MIN_QUERY_CHARS = 2
const MAX_QUERY_RUNES = 128

function SearchIcon(props) {
  return (
    <svg viewBox="0 0 24 24" width="1em" height="1em" fill="none" stroke="currentColor"
      strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" {...props}>
      <circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/>
    </svg>
  )
}
function ClearIcon(props) {
  return (
    <svg viewBox="0 0 24 24" width="1em" height="1em" fill="none" stroke="currentColor"
      strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" {...props}>
      <path d="M18 6 6 18M6 6l12 12"/>
    </svg>
  )
}

export default function LibrarySearchPanel({
  onSelect, selectedKey, disabled, onAuthRequired,
  // Active-session props — rendered in the results region when the query is
  // empty. The contracts (selection shape, polling, partId-free requests) are
  // unchanged; the panel only takes over presentation.
  sessions, sessionsLoading, sessionsError, onSelectSession, onRetrySessions,
}) {
  const [query, setQuery] = useState('')
  const [committed, setCommitted] = useState('') // debounced, min-length-gated
  const [retryCount, setRetryCount] = useState(0)
  const [results, setResults] = useState(null) // null = no search yet; [] = empty
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(null) // {kind: 'validation'|'retryable', message}
  const [hierarchy, setHierarchy] = useState([])
  const [children, setChildren] = useState(null)
  const [hierarchyLoading, setHierarchyLoading] = useState(false)
  const [hierarchyError, setHierarchyError] = useState(null)

  const abortRef = useRef(null)
  const generationRef = useRef(0)
  const hierarchyAbortRef = useRef(null)
  const hierarchyGenerationRef = useRef(0)
  const backButtonRef = useRef(null)

  // onAuthRequired arrives from App as an inline arrow (`() => setNeedsAuth(true)`),
  // so its identity changes on every parent render. Session polling on the home
  // view calls setSessions ~every 10s, re-rendering App and producing a fresh
  // callback. If the search effect listed onAuthRequired in its dependency
  // array, each poll would re-run the effect and re-fetch /library/search for
  // a query that hadn't changed — a visible refetch loop. We capture the latest
  // callback in a ref (updated during render, which is safe and idempotent for
  // refs) and call through it inside the effect. The ref object itself is
  // stable, so the effect's deps stay [committed, retryCount] and the search
  // fires only on intended inputs. 401/403 still route to whatever App passed
  // most recently.
  const onAuthRequiredRef = useRef(onAuthRequired)
  onAuthRequiredRef.current = onAuthRequired

  // Immediately abort any in-flight search and invalidate its generation on
  // every raw input edit. The debounce effect below schedules a new committed
  // query, but without this guard a request already in flight for a previous
  // committed query could resolve during the debounce window and commit stale
  // results. Bumping the generation here ensures the in-flight response's
  // generation check fails even before committed changes.
  const resetHierarchy = useCallback(() => {
    hierarchyAbortRef.current?.abort()
    hierarchyGenerationRef.current += 1
    setHierarchy([])
    setChildren(null)
    setHierarchyError(null)
    setHierarchyLoading(false)
  }, [])

  const handleQueryChange = useCallback((e) => {
    const next = e.target.value
    const normalizedChanged = next.trim() !== query.trim()
    setQuery(next)
    if (!normalizedChanged) return
    if (abortRef.current) {
      abortRef.current.abort()
      abortRef.current = null
    }
    generationRef.current += 1
    setLoading(false)
    setResults(null)
    setError(null)
    resetHierarchy()
  }, [query, resetHierarchy])

  const loadChildren = useCallback((node, nextHierarchy) => {
    hierarchyAbortRef.current?.abort()
    const controller = new AbortController()
    hierarchyAbortRef.current = controller
    const generation = ++hierarchyGenerationRef.current
    setHierarchy(nextHierarchy)
    setHierarchyLoading(true)
    setHierarchyError(null)
    setChildren(null)
    fetch(`/library/metadata/${encodeURIComponent(node.ratingKey)}/children`, {signal: controller.signal})
      .then(async response => {
        if (response.status === 401 || response.status === 403) {
          onAuthRequiredRef.current?.()
          const error = new Error('Authentication required.')
          error.kind = 'auth'
          throw error
        }
        if (!response.ok) {
          const body = await response.json().catch(() => null)
          const error = new Error(body?.error?.message || 'Couldn’t load this part of your library.')
          error.kind = response.status === 404 || response.status === 422 ? 'validation' : 'retryable'
          throw error
        }
        const data = await response.json()
        if (!Array.isArray(data)) throw new Error('Received an invalid library response.')
        return data
      })
      .then(data => {
        if (controller.signal.aborted || hierarchyGenerationRef.current !== generation) return
        setChildren(data)
      })
      .catch(nextError => {
        if (controller.signal.aborted || isAbortError(nextError) || hierarchyGenerationRef.current !== generation) return
        setHierarchyError(nextError)
      })
      .finally(() => {
        if (!controller.signal.aborted && hierarchyGenerationRef.current === generation) setHierarchyLoading(false)
      })
  }, [])

  const handleNavigate = useCallback((node) => {
    if (node?.ratingKey) loadChildren(node, [...hierarchy, node])
  }, [hierarchy, loadChildren])

  const handleHierarchyBack = useCallback(() => {
    if (hierarchy.length <= 1) {
      resetHierarchy()
      return
    }
    const nextHierarchy = hierarchy.slice(0, -1)
    loadChildren(nextHierarchy[nextHierarchy.length - 1], nextHierarchy)
  }, [hierarchy, loadChildren, resetHierarchy])

  useEffect(() => () => {
    hierarchyAbortRef.current?.abort()
    hierarchyGenerationRef.current += 1
  }, [])

  useEffect(() => {
    if (hierarchy.length) backButtonRef.current?.focus()
  }, [hierarchy.length])

  // Debounce + minimum-length gate. Below the minimum, committed clears so the
  // search effect tears down to the session list. The 128-rune bound is
  // enforced on the trimmed value so leading/trailing whitespace doesn't eat
  // into the budget; a too-long query is surfaced as validation feedback, not
  // fired.
  //
  // Rune count ([...str].length) matches the backend's utf8.RuneCountInString:
  // String.prototype.length counts UTF-16 code units, so an astral character
  // (e.g. an emoji) counts as 2 — enforcing length would reject a 64-emoji
  // query that the backend accepts at 128 runes.
  useEffect(() => {
    const trimmed = query.trim()
    const runeCount = [...trimmed].length
    if (runeCount === 0) {
      setCommitted('')
      setError(null)
      return
    }
    if (runeCount > MAX_QUERY_RUNES) {
      setCommitted('')
      setError({kind: 'validation', message: `Search is too long — keep it under ${MAX_QUERY_RUNES} characters.`})
      return
    }
    if (runeCount < MIN_QUERY_CHARS) {
      setCommitted('')
      return
    }
    const t = setTimeout(() => setCommitted(trimmed), DEBOUNCE_MS)
    return () => clearTimeout(t)
  }, [query])

  // Fire the search for the committed query. Aborts any in-flight request on
  // change/unmount so stale results can't commit.
  useEffect(() => {
    if (!committed) {
      abortRef.current?.abort()
      abortRef.current = null
      setResults(null)
      setLoading(false)
      return
    }
    const generation = ++generationRef.current
    const controller = new AbortController()
    abortRef.current = controller
    setLoading(true)
    setError(null)
    fetch(`/library/search?query=${encodeURIComponent(committed)}`, {signal: controller.signal})
      .then(r => {
        if (controller.signal.aborted || generationRef.current !== generation) return null
        if (r.status === 401 || r.status === 403) {
          // Route auth failures to the app's login handling — the search panel
          // doesn't own the auth surface. Called through the ref so a parent
          // re-render (e.g. session polling) doesn't retrigger this effect.
          onAuthRequiredRef.current?.()
          const e = new Error('Authentication required.')
          e.kind = 'auth'
          throw e
        }
        if (r.status === 422) {
          // Validation feedback from the backend (e.g. disallowed characters).
          // Non-retryable — the user must change the query.
          return r.json().then(body => {
            const e = new Error(body?.error?.message || 'That search isn’t valid.')
            e.kind = 'validation'
            throw e
          })
        }
        if (r.status === 429 || r.status >= 500) {
          const e = new Error('Plex library search is temporarily unavailable.')
          e.kind = 'retryable'
          e.status = r.status
          throw e
        }
        if (!r.ok) {
          const e = new Error(`Plex library search failed (${r.status}).`)
          e.kind = 'retryable'
          throw e
        }
        return r.json()
      })
      .then(data => {
        if (data === null) return // aborted or superseded
        if (controller.signal.aborted || generationRef.current !== generation) return
        setResults(Array.isArray(data) ? data : [])
      })
      .catch(err => {
        if (controller.signal.aborted || isAbortError(err) || generationRef.current !== generation) return
        if (err?.kind === 'auth') {
          // Auth already routed via onAuthRequired; don't surface a retryable
          // error in the panel — the app is switching to the login view.
          setError(null)
          setResults(null)
          return
        }
        if (err?.kind === 'validation') {
          setError({kind: 'validation', message: err.message})
          setResults(null)
          return
        }
        // Network failure or 429/5xx — retryable.
        setError({kind: 'retryable', message: err?.message || 'Couldn’t search the Plex library.'})
        setResults(null)
      })
      .finally(() => {
        if (!controller.signal.aborted && generationRef.current === generation) setLoading(false)
      })
    return () => {
      controller.abort()
      if (abortRef.current === controller) abortRef.current = null
    }
  // onAuthRequired is intentionally read via ref (see onAuthRequiredRef) so
  // parent re-renders don't retrigger the search. Only committed query and
  // explicit retry should re-fire.
  }, [committed, retryCount])

  const trimmedQuery = query.trim()
  const queryRuneCount = [...trimmedQuery].length
  const validLibraryQuery = queryRuneCount >= MIN_QUERY_CHARS && queryRuneCount <= MAX_QUERY_RUNES
  const searchPending = validLibraryQuery && !committed
  const searching = Boolean(committed) || searchPending
  const searchLoading = loading || searchPending
  const showResults = results != null && Boolean(committed) && !searchLoading && !error
  const empty = showResults && results.length === 0
  const validationError = error?.kind === 'validation'
  const retryableError = error?.kind === 'retryable'
  const searchOwnsRegion = searching || validationError || retryableError
  const hierarchyActive = hierarchy.length > 0

  // One concise status line for the helper text. The full messages live in the
  // status region below; this is a compact visual cue kept distinct so
  // screen-reader users don't hear the same phrase twice.
  const helperText =
    searchLoading ? 'Searching…' :
    validationError ? 'Invalid search.' :
    retryableError ? 'Search failed.' :
    empty ? 'No matches.' :
    showResults ? `${results.length} ${results.length === 1 ? 'result' : 'results'}.` :
    searching ? 'Searching…' :
    'Active sessions show here. Or type at least two characters to search your library.'

  // The results region renders exactly one of: library loading/error/empty/
  // results (when searching), or session loading/error/empty/cards (when not).
  // This is the single cohesive surface — there is no second panel.
  const sortedSessions = orderSessionsOwnedFirst(sessions)

  return (
    <Box className="cs-rise-2" sx={{display: 'flex', flexDirection: 'column', gap: {xs: 1, sm: 1.5}}}>
      <TextField
        fullWidth
        variant="outlined"
        size="small"
        placeholder="Search your Plex library…"
        label="Search Plex library"
        value={query}
        onChange={handleQueryChange}
        disabled={disabled}
        inputProps={{maxLength: MAX_QUERY_RUNES, 'aria-label': 'Search Plex library'}}
        InputProps={{
          startAdornment: <InputAdornment position="start"><SearchIcon sx={{color: 'text.secondary', fontSize: 18}}/></InputAdornment>,
          endAdornment: query ? (
            <InputAdornment position="end">
              <IconButton
                aria-label="Clear library search"
                size="small"
                edge="end"
                onClick={() => handleQueryChange({target: {value: ''}})}
                disabled={disabled}
              >
                <ClearIcon sx={{fontSize: 18}}/>
              </IconButton>
            </InputAdornment>
          ) : null,
        }}
        helperText={helperText}
        FormHelperTextProps={{sx: {color: (validationError || retryableError) ? '#ffb4a8' : 'text.disabled'}}}
      />

      {/* Single coherent status region — the one place loading / error / empty
          / results announcements live. aria-live=polite gives assistive tech a
          single, non-duplicative feed of search state. We deliberately do not
          use role="status" here because this is a persistent container that
          swaps between several states over time (a live region), not a single
          transient status message — using role="status" would collide with the
          session loading skeleton's own role="status" and confuse
          getByRole('status'). */}
      <Box
        aria-live="polite"
        aria-busy={(hierarchyActive ? hierarchyLoading : searchOwnsRegion ? searchLoading : sessionsLoading) || undefined}
        aria-label="Plex library search status"
        sx={{minHeight: 160, position: 'relative'}}
      >
        {searchLoading && !hierarchyActive && (
          <LinearProgress
            aria-hidden
            sx={{
              position: 'absolute', top: 0, left: 0, right: 0, zIndex: 1,
              height: 3, backgroundColor: 'transparent',
              '& .MuiLinearProgress-bar': {backgroundColor: '#ff7300'},
            }}
          />
        )}

        {hierarchyActive && (
          <Box sx={{mb: 2}}>
            <Button ref={backButtonRef} size="small" onClick={handleHierarchyBack} sx={{mb: 0.75, px: 0}}>← Back</Button>
            <Typography variant="body2" sx={{color: 'text.secondary'}}>
              {hierarchy.map(item => item.title).filter(Boolean).join('  /  ')}
            </Typography>
          </Box>
        )}
        {hierarchyActive && hierarchyLoading && <LinearProgress sx={{mb: 2, '& .MuiLinearProgress-bar': {backgroundColor: '#ff7300'}}}/>}
        {hierarchyActive && hierarchyError && (
          <StateMessage variant="error" title={hierarchyError.kind === 'validation' ? 'This library item is no longer available.' : 'Couldn’t load this part of your library.'} hint={hierarchyError.message}
            action={hierarchyError.kind === 'retryable' ? <Button size="small" onClick={() => loadChildren(hierarchy[hierarchy.length - 1], hierarchy)}>Try again</Button> : null}/>
        )}
        {hierarchyActive && !hierarchyLoading && !hierarchyError && children?.length === 0 && (
          <StateMessage variant="empty" title="Nothing is available here." hint="Try another season or return to the search results."/>
        )}
        {hierarchyActive && !hierarchyLoading && !hierarchyError && children?.length > 0 && (
          <Grid container spacing={{xs: 1.5, sm: 3}} sx={{mx: 'auto'}}>
            {children.map(result => <LibraryResultCard key={result.ratingKey} result={result}
              active={result.ratingKey === selectedKey} onSelect={onSelect} onNavigate={handleNavigate}/>) }
          </Grid>
        )}

        {/* --- Library search states (when a valid query is committed) --- */}
        {!hierarchyActive && validationError && (
          <StateMessage
            variant="error"
            title={error.message}
            hint="Try a shorter or different search term."
          />
        )}

        {!hierarchyActive && retryableError && (
          <StateMessage
            variant="error"
            title="Couldn’t search the Plex library."
            hint="Plex may be unreachable. Try again in a moment."
            action={
              <Box
                component="button"
                type="button"
                onClick={() => setRetryCount(n => n + 1)}
                sx={{
                  mt: 1, px: 2, py: 0.5, borderRadius: 999, cursor: 'pointer',
                  border: '1px solid rgba(255,255,255,0.2)', background: 'transparent',
                  color: '#ffd9b0', font: 'inherit', fontWeight: 600,
                }}
              >
                Try again
              </Box>
            }
          />
        )}

        {!hierarchyActive && empty && (
          <StateMessage
            variant="empty"
            title="No matching titles in your Plex library."
            hint="Try a different search term."
          />
        )}

        {!hierarchyActive && showResults && results.length > 0 && (
          <Grid container spacing={{xs: 1.5, sm: 3}} sx={{mx: 'auto'}}>
            {results.map(result => (
              <LibraryResultCard
                key={result.ratingKey}
                result={result}
                active={result.ratingKey === selectedKey}
                onSelect={onSelect}
                onNavigate={handleNavigate}
              />
            ))}
          </Grid>
        )}

        {/* --- Active-session states (empty query — the default list) --- */}
        {!hierarchyActive && !searchOwnsRegion && (
          <>
            {sessionsLoading ? (
              <SessionLoadingGrid/>
            ) : sessionsError ? (
              <SessionErrorState onRetry={onRetrySessions}/>
            ) : !sessions || sessions.length === 0 ? (
              <SessionEmptyState/>
            ) : (
              <Grid container spacing={{xs: 1.5, sm: 3}} sx={{mx: 'auto'}}>
                {sortedSessions.map(session => (
                  <SessionCard
                    key={session.ratingKey}
                    session={session}
                    active={session.ratingKey === selectedKey}
                    onSelect={onSelectSession}
                  />
                ))}
              </Grid>
            )}
          </>
        )}
      </Box>
    </Box>
  )
}
