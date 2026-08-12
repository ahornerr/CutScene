import {Box, Button, Chip, CircularProgress, Paper, Tooltip, Typography} from "@mui/material";
import {useCallback, useEffect, useRef, useState} from "react";
import StateMessage from "./StateMessage";
import {
  creatorLabel, deleteClip, fetchClip, formatClipCreated, formatClipDuration,
  mediaContext, mediaLabel,
} from "./clips";
import {CopyShareLinkButton} from "./ClipShareCopy";
import {ClipDeleteDialog} from "./ClipDeleteDialog";

// Single-clip detail view. Browser playback uses the public inline download
// URL (shareUrl === publicDownloadUrl, Content-Disposition: inline) so the
// <video> element plays without depending on cookie behaviour; the
// authenticated attachment download URL is offered as the explicit "Download"
// action. A public share link can be copied for handing to anyone.
//
// If the browser cannot load the video (network issue, codec problem, or the
// durable bytes are temporarily unavailable), a visible error fallback offers
// the authenticated download and a retry control.
export default function ClipDetail({clipId, onBack, onDeleted}) {
  const [clip, setClip] = useState(null)
  const [error, setError] = useState(null)
  const [needsAuth, setNeedsAuth] = useState(false)
  const [retryCount, setRetryCount] = useState(0)
  const [videoError, setVideoError] = useState(false)
  const [videoRetryKey, setVideoRetryKey] = useState(0)

  const [deleteOpen, setDeleteOpen] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [deleteError, setDeleteError] = useState(null)

  const abortRef = useRef(null)
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    if (abortRef.current) abortRef.current.abort()
    const controller = new AbortController()
    abortRef.current = controller
    setClip(null)
    setError(null)
    setNeedsAuth(false)
    setVideoError(false)
    fetchClip(clipId, {signal: controller.signal})
      .then(data => {
        if (!mountedRef.current || controller.signal.aborted) return
        setClip(data)
      })
      .catch(err => {
        if (!mountedRef.current || controller.signal.aborted) return
        if (err?.code === 'auth') { setNeedsAuth(true); return }
        setError(err)
      })
    return () => {
      mountedRef.current = false
      if (abortRef.current) abortRef.current.abort()
    }
  }, [clipId, retryCount])

  const confirmDelete = useCallback(async () => {
    if (!clip) return
    setDeleting(true)
    setDeleteError(null)
    try {
      await deleteClip(clip.id)
      if (!mountedRef.current) return
      setDeleteOpen(false)
      setDeleting(false)
      onDeleted?.(clip.id)
    } catch (err) {
      if (!mountedRef.current) return
      setDeleting(false)
      if (err?.code === 'not_found') {
        setDeleteOpen(false)
        onDeleted?.(clip.id)
        return
      }
      // Auth and network errors both stay in the dialog; the dialog itself
      // renders the appropriate recovery action (reload vs. retry).
      setDeleteError(err)
    }
  }, [clip, onDeleted])

  const cancelDelete = useCallback(() => {
    if (deleting) return
    setDeleteOpen(false)
    setDeleteError(null)
  }, [deleting])

  const loading = clip === null && !error && !needsAuth
  // The backend detail response carries an explicit `isAdmin` boolean (only
  // present for the authenticated caller). Use it instead of inferring admin
  // from ownerUuid presence — a regular creator also has ownerUuid on their
  // own clip and must not see an admin-style creator chip.
  const admin = clip?.isAdmin === true
  const canDelete = clip?.canDelete === true
  const creator = admin ? creatorLabel(clip) : ''
  const {primary: mediaPrimary, secondary: mediaSecondary} = mediaLabel(clip)
  const displayTitle = clip?.title || 'Untitled clip'
  // shareUrl and publicDownloadUrl are the same stable public inline MP4 URL.
  const playbackSrc = clip?.publicDownloadUrl || clip?.shareUrl || ''
  const downloadUrl = clip?.downloadUrl || ''
  const artworkUrl = clip?.artworkUrl || ''

  return (
    <Box className="cs-rise" sx={{display: 'flex', flexDirection: 'column', gap: {xs: 2, sm: 2.5}}}>
      <Box sx={{display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap'}}>
        {onBack && (
          <Button
            variant="outlined"
            color="primary"
            onClick={onBack}
            sx={{borderColor: 'rgba(255,255,255,0.2)', px: 2, py: 0.75}}
          >
            Back to library
          </Button>
        )}
      </Box>

      {loading && (
        <Box role="status" aria-live="polite" aria-busy="true" sx={{py: 4, display: 'flex', justifyContent: 'center'}}>
          <CircularProgress thickness={3}/>
          <Typography variant="caption" sx={visuallyHidden}>Loading clip…</Typography>
        </Box>
      )}

      {needsAuth && (
        <StateMessage
          variant="error"
          title="Sign in to view this clip."
          hint="Reload the page and connect CutScene to Plex."
          live
        />
      )}

      {error && !needsAuth && (
        <StateMessage
          variant={error?.code === 'not_found' ? 'empty' : 'error'}
          title={error?.code === 'not_found' ? 'This clip is no longer available.' : 'Couldn’t load this clip.'}
          hint={error?.code === 'not_found'
            ? 'It may have been deleted by its creator or an administrator.'
            : (error?.message || 'Try again in a moment.')}
          live
          action={error?.code !== 'not_found' && (
            <Button variant="outlined" color="primary" size="small"
              onClick={() => setRetryCount(n => n + 1)}
              sx={{mt: 1, borderColor: 'rgba(255,255,255,0.2)'}}
            >
              Try again
            </Button>
          )}
        />
      )}

      {clip && (
        <Box sx={{display: 'flex', flexDirection: 'column', gap: {xs: 2, sm: 2.5}}}>
          <Box sx={{minWidth: 0}}>
            <Typography variant="overline" className="cs-section-label" sx={{color: '#ff7300'}}>
              Clip
            </Typography>
            <Typography variant="h4" sx={{mt: 0.5, overflowWrap: 'anywhere', fontSize: {xs: '1.55rem', sm: '2.125rem'}, lineHeight: 1.15}}>
              {displayTitle}
            </Typography>
            {(mediaPrimary || mediaSecondary) && (
              <Typography variant="subtitle1" sx={{color: 'text.secondary', mt: 0.5, wordBreak: 'break-word'}}>
                {mediaContext(clip)}
              </Typography>
            )}
            <Box sx={{display: 'flex', flexWrap: 'wrap', gap: 1, alignItems: 'center', mt: 1}}>
              {formatClipDuration(clip) && (
                <Typography variant="caption" sx={{fontFamily: 'var(--cs-mono-font)', color: '#ff7300'}}>
                  {formatClipDuration(clip)}
                </Typography>
              )}
              {formatClipCreated(clip) && (
                <Typography variant="caption" sx={{color: 'text.secondary'}}>
                  Created {formatClipCreated(clip)}
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
                    }}
                  />
                </Tooltip>
              )}
            </Box>
          </Box>

          {/* Browser playback. The inline public download URL plays in-page.
              If the browser cannot load the video, a visible error fallback
              offers the authenticated download and a retry control. */}
          <Paper
            variant="outlined"
            sx={{
              p: 0, overflow: 'hidden', borderRadius: 2,
              borderColor: 'rgba(255,255,255,0.08)',
              backgroundColor: '#000',
              boxShadow: '0 24px 60px -28px rgba(0,0,0,0.8)',
            }}
          >
            {playbackSrc && !videoError ? (
              <Box
                key={videoRetryKey}
                component="video"
                src={playbackSrc}
                controls
                preload="metadata"
                poster={artworkUrl || undefined}
                aria-label={`Video player for ${displayTitle}`}
                onError={() => setVideoError(true)}
                sx={{width: '100%', aspectRatio: '16 / 9', maxHeight: {xs: '56vh', sm: '70vh'}, display: 'block', background: '#000', objectFit: 'contain'}}
              />
            ) : videoError ? (
              <Box sx={{p: 4, textAlign: 'center', display: 'flex', flexDirection: 'column', gap: 1.5, alignItems: 'center'}}>
                <Typography variant="body2" sx={{color: '#ffb4a8'}} role="alert">
                  Couldn’t play this clip in the browser.
                </Typography>
                <Typography variant="caption" sx={{color: 'text.secondary', maxWidth: 420}}>
                  The video may be temporarily unavailable or your browser can’t decode it. You can still download the file and play it locally.
                </Typography>
                <Box sx={{display: 'flex', gap: 1.5, mt: 0.5, flexWrap: 'wrap', justifyContent: 'center'}}>
                  {downloadUrl && (
                    <Button variant="contained" color="primary" href={downloadUrl} download sx={{px: 3, py: 1}}>
                      Download clip
                    </Button>
                  )}
                  <Button
                    variant="outlined"
                    color="primary"
                    onClick={() => { setVideoError(false); setVideoRetryKey(k => k + 1) }}
                    sx={{borderColor: 'rgba(255,255,255,0.2)', px: 2.5, py: 1}}
                  >
                    Try again
                  </Button>
                </Box>
              </Box>
            ) : (
              <Box sx={{p: 4, textAlign: 'center'}}>
                <Typography variant="body2" sx={{color: 'text.secondary'}}>
                  Playback is unavailable for this clip.
                </Typography>
              </Box>
            )}
          </Paper>

          <Box sx={{display: 'flex', flexWrap: 'wrap', gap: 1, alignItems: 'center'}}>
            {downloadUrl && (
              <Button
                variant="contained"
                color="primary"
                href={downloadUrl}
                download
                sx={{px: 3, py: 1, width: {xs: '100%', sm: 'auto'}}}
              >
                Download clip
              </Button>
            )}
            <CopyShareLinkButton shareUrl={clip.shareUrl} />
            <Box sx={{display: {xs: 'none', sm: 'block'}, flex: 1}}/>
            {canDelete ? (
              <Tooltip title="Delete this clip permanently">
                <Button
                  variant="outlined"
                  onClick={() => setDeleteOpen(true)}
                  sx={{
                    px: 2.5, py: 1, width: {xs: '100%', sm: 'auto'}, color: '#ffb4a8',
                    borderColor: 'rgba(255,90,90,0.4)',
                    '&:hover': {borderColor: 'rgba(255,90,90,0.7)', backgroundColor: 'rgba(255,90,90,0.08)'},
                  }}
                  aria-label={`Delete clip ${displayTitle}`}
                >
                  Delete clip
                </Button>
              </Tooltip>
            ) : clip && (
              <Tooltip title="You don’t have permission to delete this clip." arrow>
                <span>
                  <Button
                    variant="outlined"
                    disabled
                    sx={{
                      px: 2.5, py: 1, color: 'text.disabled',
                      borderColor: 'rgba(255,255,255,0.12)',
                    }}
                    aria-label="Delete unavailable"
                  >
                    Delete clip
                  </Button>
                </span>
              </Tooltip>
            )}
          </Box>

          {clip.shareUrl && (
            <Paper variant="outlined" sx={{p: {xs: 1.25, sm: 1.5}, borderColor: 'rgba(255,255,255,0.08)', backgroundColor: 'rgba(0,0,0,0.16)'}}>
              <Typography variant="caption" sx={{color: 'text.secondary', display: 'block', mb: 0.5}}>
                Public share link
              </Typography>
              <Typography sx={{fontFamily: 'var(--cs-mono-font)', fontSize: {xs: '0.75rem', sm: '0.82rem'}, overflowWrap: 'anywhere', wordBreak: 'break-word', color: 'text.primary', lineHeight: 1.55}}>
                {clip.shareUrl}
              </Typography>
              <Typography variant="caption" sx={{color: 'text.disabled', display: 'block', mt: 0.5}}>
                Anyone with this link can play and download the clip.
              </Typography>
            </Paper>
          )}
        </Box>
      )}

      <ClipDeleteDialog
        open={deleteOpen}
        clip={clip}
        deleting={deleting}
        error={deleteError}
        onConfirm={confirmDelete}
        onClose={cancelDelete}
      />
    </Box>
  )
}

const visuallyHidden = {
  position: 'absolute', width: 1, height: 1, padding: 0, margin: -1,
  overflow: 'hidden', clip: 'rect(0, 0, 0, 0)', whiteSpace: 'nowrap', border: 0,
}
