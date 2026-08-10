import {Box, Button, Chip, CircularProgress, IconButton, Paper, Stack, Tooltip, Typography} from "@mui/material";
import {useCallback, useEffect, useRef, useState} from "react";
import StateMessage from "./StateMessage";
import {
  creatorLabel, deleteClip, fetchClips, formatClipCreated, formatClipDuration,
  mediaContext,
} from "./clips";
import {CopyShareLinkButton} from "./ClipShareCopy";
import {ClipDeleteDialog} from "./ClipDeleteDialog";

// Authenticated clip library. Lists the caller's saved clips (or, for a
// server administrator, every clip on the server with creator differentiation).
//
// `isAdmin` is an explicit boolean on the list response. `canDelete` is an
// explicit boolean per clip — the delete affordance is gated on it so a
// viewer who cannot delete never sees the control. `creatorDisplayName` is the
// friendly creator identity (never a UUID) shown only in admin scope.
export default function ClipLibrary({onOpenClip, onBack, hasActiveWorkspace}) {
  const [clips, setClips] = useState(null)
  const [isAdmin, setIsAdmin] = useState(false)
  const [error, setError] = useState(null)
  const [needsAuth, setNeedsAuth] = useState(false)
  const [retryCount, setRetryCount] = useState(0)

  const [pendingDelete, setPendingDelete] = useState(null)
  const [deleteError, setDeleteError] = useState(null)
  const [deleting, setDeleting] = useState(false)
  const [toast, setToast] = useState(null)

  const abortRef = useRef(null)
  const mountedRef = useRef(true)

  const load = useCallback(() => {
    if (abortRef.current) abortRef.current.abort()
    const controller = new AbortController()
    abortRef.current = controller
    setClips(null)
    setError(null)
    setNeedsAuth(false)
    fetchClips({signal: controller.signal})
      .then(({clips: data, isAdmin: admin}) => {
        if (!mountedRef.current || controller.signal.aborted) return
        setClips(data)
        setIsAdmin(admin)
      })
      .catch(err => {
        if (!mountedRef.current || controller.signal.aborted) return
        if (err?.code === 'auth') {
          setNeedsAuth(true)
          return
        }
        setError(err)
      })
  }, [])

  useEffect(() => {
    mountedRef.current = true
    load()
    return () => {
      mountedRef.current = false
      if (abortRef.current) abortRef.current.abort()
    }
  }, [load, retryCount])

  const requestDelete = useCallback((clip) => {
    setDeleteError(null)
    setPendingDelete(clip)
  }, [])

  const confirmDelete = useCallback(async () => {
    const clip = pendingDelete
    if (!clip) return
    setDeleting(true)
    setDeleteError(null)
    try {
      await deleteClip(clip.id)
      if (!mountedRef.current) return
      setPendingDelete(null)
      setDeleting(false)
      setClips(prev => prev ? prev.filter(c => c.id !== clip.id) : prev)
      setToast({kind: 'success', message: 'Clip deleted.'})
    } catch (err) {
      if (!mountedRef.current) return
      setDeleting(false)
      if (err?.code === 'not_found') {
        // Already gone — close the dialog and reconcile the list.
        setPendingDelete(null)
        setClips(prev => prev ? prev.filter(c => c.id !== clip.id) : prev)
        setToast({kind: 'success', message: 'Clip already removed.'})
        return
      }
      // Auth and network errors both stay in the dialog; the dialog itself
      // renders the appropriate recovery action (reload vs. retry).
      setDeleteError(err)
    }
  }, [pendingDelete])

  const cancelDelete = useCallback(() => {
    if (deleting) return
    setPendingDelete(null)
    setDeleteError(null)
  }, [deleting])

  const dismissToast = useCallback(() => setToast(null), [])

  const loading = clips === null && !error && !needsAuth

  return (
    <Box className="cs-rise" sx={{display: 'flex', flexDirection: 'column', gap: 2.5}}>
      <Box sx={{display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', gap: 2, flexWrap: 'wrap'}}>
        <Box sx={{minWidth: 0}}>
          <Typography variant="overline" className="cs-section-label" sx={{color: '#ff7300'}}>
            Library
          </Typography>
          <Typography variant="h4" sx={{mt: 0.5}}>
            {isAdmin ? 'All clips' : 'Your clips'}
          </Typography>
          <Typography variant="body2" sx={{color: 'text.secondary', mt: 0.5, maxWidth: 560}}>
            {clips && clips.length > 0
              ? (isAdmin
                  ? 'Every saved clip on this server. You can play, share, or delete any clip.'
                  : 'Saved renders stay here until you delete them. Play, copy a public share link, or remove a clip.')
              : 'Saved renders are kept here so you can replay, share, or download them later.'}
          </Typography>
        </Box>
        {onBack && (
          <Button
            variant="outlined"
            color="primary"
            onClick={onBack}
            sx={{borderColor: 'rgba(255,255,255,0.2)', px: 2, py: 0.75}}
          >
            {hasActiveWorkspace ? 'Back to editing' : 'Back to sessions'}
          </Button>
        )}
      </Box>

      {loading && (
        <Box role="status" aria-live="polite" aria-busy="true" sx={{py: 4}}>
          <Stack spacing={2}>
            {[0, 1, 2].map(i => (
              <Paper key={i} variant="outlined" sx={{p: 2, borderColor: 'rgba(255,255,255,0.06)', display: 'flex', gap: 2, alignItems: 'center'}}>
                <Box sx={{width: 96, height: 54, borderRadius: 1, background: 'linear-gradient(110deg,#222633,#1b1e26)'}}/>
                <Box sx={{flex: 1, display: 'flex', flexDirection: 'column', gap: 1}}>
                  <Box sx={{height: 18, width: '46%', borderRadius: 1, background: '#2a2f3a'}}/>
                  <Box sx={{height: 12, width: '28%', borderRadius: 1, background: '#232733'}}/>
                </Box>
                <CircularProgress size={20} thickness={3} sx={{color: 'text.disabled'}}/>
              </Paper>
            ))}
          </Stack>
          <Typography variant="caption" sx={visuallyHidden}>Loading clips…</Typography>
        </Box>
      )}

      {needsAuth && (
        <StateMessage
          variant="error"
          title="Sign in to view your clips."
          hint="Reload the page and connect CutScene to Plex to see your clip library."
          live
        />
      )}

      {error && !needsAuth && (
        <StateMessage
          variant="error"
          title="Couldn’t load the clip library."
          hint={error?.message || 'Check that the CutScene server is reachable and try again.'}
          live
          action={
            <Button variant="outlined" color="primary" size="small"
              onClick={() => setRetryCount(n => n + 1)}
              sx={{mt: 1, borderColor: 'rgba(255,255,255,0.2)'}}
            >
              Try again
            </Button>
          }
        />
      )}

      {clips && clips.length === 0 && !needsAuth && (
        <StateMessage
          variant="empty"
          title="No saved clips yet."
          hint="Render a clip from an active session and it will appear here automatically."
          live
        />
      )}

      {clips && clips.length > 0 && (
        <Stack spacing={1.5} aria-label="Saved clips">
          {clips.map(clip => (
            <ClipRow
              key={clip.id}
              clip={clip}
              admin={isAdmin}
              onOpen={() => onOpenClip?.(clip.id)}
              onDelete={() => requestDelete(clip)}
            />
          ))}
        </Stack>
      )}

      <ClipDeleteDialog
        open={Boolean(pendingDelete)}
        clip={pendingDelete}
        deleting={deleting}
        error={deleteError}
        onConfirm={confirmDelete}
        onClose={cancelDelete}
      />

      {toast && (
        <Box
          role="status"
          aria-live="polite"
          sx={{
            position: 'fixed', left: '50%', bottom: 24, transform: 'translateX(-50%)',
            zIndex: 60, px: 2.5, py: 1.25, borderRadius: 999,
            backgroundColor: toast.kind === 'error' ? 'rgba(120,30,30,0.92)' : 'rgba(46,90,60,0.92)',
            color: toast.kind === 'error' ? '#ffd9d2' : '#d6f5dd',
            fontSize: '0.85rem', fontWeight: 600,
            boxShadow: '0 12px 30px -12px rgba(0,0,0,0.7)',
            display: 'flex', alignItems: 'center', gap: 1.5,
          }}
        >
          {toast.message}
          <Button size="small" onClick={dismissToast} sx={{color: 'inherit', minWidth: 0, padding: 0, fontWeight: 700}}>
            Dismiss
          </Button>
        </Box>
      )}
    </Box>
  )
}

function ClipRow({clip, admin, onOpen, onDelete}) {
  const created = formatClipCreated(clip)
  const duration = formatClipDuration(clip)
  const creator = admin ? creatorLabel(clip) : ''
  const shareUrl = clip.shareUrl || ''
  const canDelete = clip.canDelete === true
  const context = mediaContext(clip)
  const artworkUrl = clip.artworkUrl || ''
  const title = clip.title || 'Untitled clip'

  // The row is a non-interactive container. The clickable area (thumbnail +
  // title + metadata) is a dedicated button; the action controls are siblings,
  // not children. This eliminates nested interactive elements and keyboard
  // event bubbling while preserving the visual layout.
  const openLabel = `Open clip ${title}${context ? `, ${context}` : ''}${duration ? `, ${duration}` : ''}${created ? `, created ${created}` : ''}`

  return (
    <Paper
      variant="outlined"
      sx={{
        p: 0, overflow: 'hidden', borderColor: 'rgba(255,255,255,0.08)',
        transition: 'border-color 180ms ease, box-shadow 180ms ease, transform 180ms ease',
        '&:hover': {borderColor: 'rgba(255,115,0,0.4)', transform: 'translateY(-1px)'},
      }}
    >
      <Box sx={{display: 'flex', alignItems: 'stretch', width: '100%'}}>
        {/* Clickable area — a single button, no interactive children. */}
        <Box
          component="button"
          type="button"
          onClick={onOpen}
          aria-label={openLabel}
          sx={{
            display: 'flex', alignItems: 'center', gap: {xs: 1.5, sm: 2}, flex: 1, minWidth: 0,
            p: 1.5, border: 0, background: 'transparent', color: 'inherit',
            cursor: 'pointer', textAlign: 'left', fontFamily: 'inherit', fontSize: 'inherit',
            '&:focus-visible': {outline: '2px solid #ff7300', outlineOffset: '-2px', borderRadius: 1},
          }}
        >
          <ClipThumbnail artworkUrl={artworkUrl} />

          <Box sx={{flex: 1, minWidth: 0}}>
            <Typography noWrap sx={{fontWeight: 700, fontSize: {xs: '0.95rem', sm: '1.02rem'}, lineHeight: 1.3}}>
              {title}
            </Typography>
            {context && (
              <Typography noWrap variant="body2" sx={{color: 'text.secondary', mt: -0.25}}>
                {context}
              </Typography>
            )}
            <Box sx={{display: 'flex', flexWrap: 'wrap', gap: 1, alignItems: 'center', mt: 0.5}}>
              {duration && (
                <Typography component="span" variant="caption" sx={{fontFamily: 'var(--cs-mono-font)', color: 'text.secondary'}}>
                  {duration}
                </Typography>
              )}
              {created && (
                <Typography component="span" variant="caption" sx={{color: 'text.disabled'}}>
                  {created}
                </Typography>
              )}
              {admin && (
                <Tooltip title={creator === 'Unknown creator' ? 'Creator name unavailable for this clip' : `Creator ${creator}`} arrow>
                  <Chip
                    size="small"
                    label={`by ${creator}`}
                    sx={{
                      height: 20, fontSize: '0.7rem',
                      backgroundColor: 'rgba(255,115,0,0.10)', color: '#ffd9b0',
                      border: '1px solid rgba(255,115,0,0.28)', fontWeight: 600,
                      maxWidth: {xs: 120, sm: 200},
                    }}
                  />
                </Tooltip>
              )}
            </Box>
          </Box>
        </Box>
      </Box>
      <ClipRowActions
        shareUrl={shareUrl}
        canDelete={canDelete}
        clipTitle={title}
        onDelete={onDelete}
      />
    </Paper>
  )
}

// Action controls rendered as a sibling row beneath the open button. This
// avoids any outer clipped affordance: on narrow widths the actions wrap to
// their own line with adequate space, and the delete icon is never clipped
// by the row's horizontal bounds.
function ClipRowActions({shareUrl, canDelete, clipTitle, onDelete}) {
  return (
    <Box
      sx={{
        display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap',
        px: 1.5, pb: 1.5, pt: 0.25,
        borderTop: '1px solid rgba(255,255,255,0.04)',
      }}
    >
      <CopyShareLinkButton shareUrl={shareUrl} size="small" />
      <Box sx={{flex: 1}}/>
      {canDelete ? (
        <Tooltip title="Delete clip">
          <IconButton
            aria-label={`Delete clip ${clipTitle}`}
            onClick={onDelete}
            size="small"
            sx={{color: 'text.secondary', '&:hover': {color: '#ffb4a8', backgroundColor: 'rgba(255,90,90,0.10)'}}}
          >
            <DeleteIcon/>
          </IconButton>
        </Tooltip>
      ) : (
        <Tooltip title="You don’t have permission to delete this clip." arrow>
          <span>
            <IconButton
              disabled
              aria-label="Delete unavailable"
              size="small"
              sx={{color: 'text.disabled', opacity: 0.4}}
            >
              <DeleteIcon/>
            </IconButton>
          </span>
        </Tooltip>
      )}
    </Box>
  )
}

// Durable thumbnail with graceful placeholder and image-error fallback.
// The artwork is a snapshot captured at render-promotion time and served from
// the authenticated /clips/:id/artwork endpoint. If the image fails to load
// (network, missing file, decode error), the placeholder reappears.
//
// The thumbnail renders inside the row's native open button, so the placeholder
// is purely decorative (no nested interactive element). Artwork retries happen
// naturally on URL change (row reuse) or row re-render; a nested retry control
// would be both invalid HTML and redundant with the opener.
function ClipThumbnail({artworkUrl}) {
  const [failed, setFailed] = useState(false)

  // Reset the failed state if the artwork URL changes (e.g. row reuse).
  useEffect(() => { setFailed(false) }, [artworkUrl])

  const showImage = artworkUrl && !failed

  return (
    <Box sx={{
      width: {xs: 80, sm: 96}, height: {xs: 45, sm: 54}, flexShrink: 0,
      borderRadius: 1, overflow: 'hidden', position: 'relative',
      background: 'linear-gradient(135deg,#222633,#11141a)',
      display: 'grid', placeItems: 'center',
    }}>
      {showImage ? (
        // Decorative image: the enclosing open button carries the full
        // aria-label, so the thumbnail alt is empty to avoid redundancy.
        // eslint-disable-next-line jsx-a11y/alt-text
        <Box
          component="img"
          src={artworkUrl}
          alt=""
          onError={() => setFailed(true)}
          sx={{width: '100%', height: '100%', objectFit: 'cover', display: 'block'}}
        />
      ) : (
        <svg viewBox="0 0 24 24" width="26" height="26" fill="none" stroke="rgba(255,255,255,0.22)"
          strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
          <polygon points="6 4 20 12 6 20 6 4"/>
        </svg>
      )}
    </Box>
  )
}

function DeleteIcon() {
  return (
    <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor"
      strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6M10 11v6M14 11v6"/>
    </svg>
  )
}

const visuallyHidden = {
  position: 'absolute', width: 1, height: 1, padding: 0, margin: -1,
  overflow: 'hidden', clip: 'rect(0, 0, 0, 0)', whiteSpace: 'nowrap', border: 0,
}