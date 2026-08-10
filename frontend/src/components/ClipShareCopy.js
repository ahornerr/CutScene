import {Button, CircularProgress, IconButton, Tooltip, Typography} from "@mui/material";
import {useCallback, useEffect, useRef, useState} from "react";
import {copyToClipboard} from "./clips";

// Copy a public share link to the clipboard. The backend mints an unguessable
// share token per clip; shareUrl is the public `/shared/clips/:token` URL.
// We show a brief "Copied" confirmation rather than a toast so the affordance
// sits inline next to the link it copies.
export function CopyShareLinkButton({shareUrl, size = 'medium', label = 'Copy share link'}) {
  const [state, setState] = useState('idle') // 'idle' | 'copying' | 'copied' | 'error'
  const resetTimerRef = useRef(null)

  useEffect(() => () => {
    if (resetTimerRef.current) clearTimeout(resetTimerRef.current)
  }, [])

  const handleCopy = useCallback(async () => {
    if (!shareUrl) return
    setState('copying')
    const ok = await copyToClipboard(shareUrl)
    setState(ok ? 'copied' : 'error')
    if (resetTimerRef.current) clearTimeout(resetTimerRef.current)
    resetTimerRef.current = setTimeout(() => setState('idle'), 2500)
  }, [shareUrl])

  if (!shareUrl) {
    // No share token available — render a disabled control so the affordance
    // is discoverable but clearly unavailable.
    return (
      <Tooltip title="Sharing is unavailable for this clip." arrow>
        <span>
          <IconButton disabled size={size} aria-label="Share link unavailable">
            <ShareIcon/>
          </IconButton>
        </span>
      </Tooltip>
    )
  }

  const ariaLabel = state === 'copied' ? `${label} copied` : label

  if (size === 'small') {
    return (
      <Tooltip
        title={state === 'copied' ? 'Link copied to clipboard' : (state === 'error' ? 'Copy failed — select and copy manually' : 'Copy public share link')}
        arrow
      >
        <IconButton
          aria-label={ariaLabel}
          onClick={handleCopy}
          size="small"
          disabled={state === 'copying'}
          sx={{
            color: state === 'copied' ? '#7fd391' : state === 'error' ? '#ffb4a8' : 'text.secondary',
            '&:hover': {color: '#ffd9b0', backgroundColor: 'rgba(255,115,0,0.10)'},
          }}
        >
          {state === 'copying' ? <CircularProgress size={16} thickness={3}/> : <ShareIcon checked={state === 'copied'}/>}
        </IconButton>
      </Tooltip>
    )
  }

  return (
    <Button
      variant="outlined"
      color="primary"
      onClick={handleCopy}
      disabled={state === 'copying'}
      startIcon={state === 'copied' ? <CheckIcon/> : <ShareIcon/>}
      sx={{borderColor: 'rgba(255,255,255,0.2)', px: 2, py: 0.75}}
      aria-label={ariaLabel}
      aria-live="polite"
    >
      {state === 'copied' ? 'Link copied' : state === 'error' ? 'Copy failed' : label}
    </Button>
  )
}

function ShareIcon({checked}) {
  return (
    <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor"
      strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      {checked ? (
        <path d="M20 6 9 17l-5-5"/>
      ) : (
        <>
          <circle cx="18" cy="5" r="3"/>
          <circle cx="6" cy="12" r="3"/>
          <circle cx="18" cy="19" r="3"/>
          <path d="M8.6 13.5 15.4 17.5M15.4 6.5 8.6 10.5"/>
        </>
      )}
    </svg>
  )
}

function CheckIcon() {
  return (
    <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor"
      strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <path d="M20 6 9 17l-5-5"/>
    </svg>
  )
}