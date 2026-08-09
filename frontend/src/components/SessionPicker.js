import {Box, Button, Card, CardActionArea, CardContent, CardMedia, Grid, LinearProgress, Typography} from "@mui/material";
import {useMemo} from "react";
import StateMessage from "./StateMessage";

const visuallyHidden = {
  position: 'absolute',
  width: 1,
  height: 1,
  padding: 0,
  margin: -1,
  overflow: 'hidden',
  clip: 'rect(0, 0, 0, 0)',
  whiteSpace: 'nowrap',
  border: 0,
};

function millisToDuration(millis) {
  const hours = Math.floor(millis / 1000 / 60 / 60)
  const minutes = Math.floor(millis / 1000 / 60) % 60
  const seconds = Math.floor(millis / 1000) % 60
  return String(hours).padStart(2, '0') + ':' + String(minutes).padStart(2, '0') + ':' + String(seconds).padStart(2, '0')
}

function getVideoName(session) {
  if (session.type === "episode") {
    return {
      top: session.grandparentTitle,
      bottom: `S${String(session.parentIndex).padStart(2, '0')}E${String(session.index).padStart(2, '0')} ${session.title}`,
    }
  }
  return {top: `${session.title}`, bottom: session.year ? `(${session.year})` : ''}
}

// Map Plex player state → a colored status dot + short label. All states are
// existing fields from the session API; nothing is invented.
function getPlayerState(state) {
  switch (state) {
    case 'playing': return {color: '#7fd391', label: 'Playing'}
    case 'paused': return {color: '#ffd9a0', label: 'Paused'}
    case 'buffering': return {color: '#8fb8ff', label: 'Buffering'}
    default: return null
  }
}

// Audio channels → human layout. The API delivers a channel count; the 5.1/7.1
// mapping is the standard industry convention (5.1 = 6 channels incl. LFE),
// not an inferred detail. Defensive for missing/odd values.
function audioLayoutLabel(channels) {
  if (channels == null) return null
  const n = Number(channels)
  if (!Number.isFinite(n) || n <= 0) return null
  if (n === 1) return '1.0'
  if (n === 2) return '2.0'
  if (n === 6) return '5.1'
  if (n === 8) return '7.1'
  return `${n}ch`
}

// Resolution → tidy uppercase chip text. Plex sends values like "4k", "1080",
// "720", "sd". Uppercase the raw value; do not synthesize a "p" suffix since
// the API does not distinguish interlaced vs progressive.
function resolutionLabel(res) {
  if (!res) return null
  const s = String(res).trim()
  return s ? s.toUpperCase() : null
}

// Combine resolution + audio layout into one compact quality summary, e.g.
// "1080 · 5.1". Each part optional; returns null when neither is present so
// the pill can be omitted entirely.
function qualitySummary(res, audio) {
  const parts = [res, audio].filter(Boolean)
  return parts.length ? parts.join(' · ') : null
}

// Session.location ("lan" | "wan") → local/remote label.
function locationLabel(loc) {
  if (loc === 'lan') return 'Local'
  if (loc === 'wan') return 'Remote'
  return null
}

// Inline film placeholder — used when neither thumb nor grandparentThumb is
// available, so the card never emits a broken image request.
function FilmPlaceholder() {
  return (
    <Box sx={{
      position: 'absolute', inset: 0, display: 'grid', placeItems: 'center',
      background: 'linear-gradient(135deg, #1b1e26, #11141a)',
    }}>
      <svg viewBox="0 0 24 24" width="42" height="42" fill="none" stroke="rgba(255,255,255,0.18)"
        strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
        <rect x="3" y="4" width="18" height="16" rx="2"/>
        <path d="M3 9h18M3 15h18M8 4v16M16 4v16"/>
      </svg>
    </Box>
  )
}

export default function SessionPicker({sessions, loading, error, selectedKey, onSelect, onRetry}) {
  // Stable owned-first ordering: owned sessions surface first so the user's
  // own sessions lead the scan, while the original relative order is preserved
  // within each group (stable partition, no secondary sort key). This hook must
  // run before every conditional return.
  const orderedSessions = useMemo(() => {
    if (!sessions) return sessions
    return sessions.reduce((acc, session) => {
      (session.ownedByCurrentUser === true ? acc.owned : acc.rest).push(session)
      return acc
    }, {owned: [], rest: []})
  }, [sessions])
  const sortedSessions = orderedSessions ? [...orderedSessions.owned, ...orderedSessions.rest] : orderedSessions

  if (loading) {
    return (
      <>
        <Box component="span" role="status" aria-live="polite" sx={visuallyHidden}>
          Loading sessions…
        </Box>
        <Grid
          container
          spacing={3}
          sx={{mx: 'auto', px: {xs: 2, sm: 3}}}
          aria-busy="true"
          aria-label="Loading active Plex sessions"
        >
          {[0, 1, 2, 3, 4, 5].map(i => (
            <Grid item xs={12} sm={6} md={4} key={i}>
              <Card sx={{height: '100%', display: 'flex', flexDirection: 'column', overflow: 'hidden'}}>
                <Box sx={{
                  width: '100%', aspectRatio: '16 / 9', flexShrink: 0,
                  background: 'linear-gradient(110deg,#222633,#1b1e26)',
                  borderBottom: '1px solid rgba(255,255,255,0.06)',
                }}/>
                <CardContent sx={{display: 'flex', flexDirection: 'column', gap: 1.25}}>
                  <Box sx={{height: 24, width: '76%', borderRadius: 1, background: '#2a2f3a'}}/>
                  <Box sx={{height: 16, width: '42%', borderRadius: 1, background: '#232733', mb: 0.5}}/>
                  <Box sx={{height: 6, width: '100%', borderRadius: 999, background: '#232733', mb: 0.75}}/>
                  <Box sx={{height: 8, width: '38%', borderRadius: 999, background: '#2a2f3a'}}/>
                  <Box sx={{height: 12, width: '64%', borderRadius: 1, background: '#232733', mt: 0.5}}/>
                </CardContent>
              </Card>
            </Grid>
          ))}
        </Grid>
      </>
    )
  }

  if (error) {
    return (
      <StateMessage
        variant="error"
        title="Couldn’t load your Plex sessions."
        hint="Check that the CutScene server is reachable and Plex is linked."
        live
        action={onRetry && (
          <Button variant="outlined" color="primary" size="small" onClick={onRetry}
            sx={{mt: 1, borderColor: 'rgba(255,255,255,0.2)'}}>
            Try again
          </Button>
        )}
      />
    )
  }

  if (!sessions || sessions.length === 0) {
    return (
      <StateMessage
        variant="empty"
        title="No active Plex sessions."
        hint="Start playing something in Plex and it will appear here."
        live
      />
    )
  }

  return (
    <Grid container spacing={3} sx={{mx: 'auto', px: {xs: 2, sm: 3}}}>
      {sortedSessions.map((session, index) => {
        const name = getVideoName(session)
        const active = session.ratingKey === selectedKey
        // The session API tags sessions owned by the current user with
        // `ownedByCurrentUser: true`. Treat absent/false as not owned so the
        // UI degrades gracefully when the field isn't supplied.
        const owned = session.ownedByCurrentUser === true

        // Existing session metadata, surfaced defensively — each is optional.
        const duration = session.duration
        const viewOffset = session.viewOffset
        const hasProgress = duration != null && viewOffset != null && duration > 0
        const progressPct = hasProgress ? Math.min(100, Math.max(0, (viewOffset / duration) * 100)) : 0
        const playerState = getPlayerState(session.Player?.state)
        const playerTitle = session.Player?.title
        const res = resolutionLabel(session.Media?.[0]?.videoResolution)
        const audio = audioLayoutLabel(session.Media?.[0]?.audioChannels)
        const quality = qualitySummary(res, audio)
        const loc = locationLabel(session.Session?.location)
        const userTitle = session.User?.title
        // Thumbnail: episode still first, fall back to the show poster
        // (grandparentThumb) when the episode has no art. If neither exists,
        // render a placeholder instead of a broken image request.
        const thumbPath = session.thumb || session.grandparentThumb || null

        // Muted footer line — supporting context, dot-separated. The user is
        // omitted on owned cards because the "Your session" badge already
        // conveys ownership; showing your own name would be redundant.
        const footerParts = []
        if (!owned && userTitle) footerParts.push(userTitle)
        if (playerTitle) footerParts.push(playerTitle)
        if (loc) footerParts.push(loc)
        const footer = footerParts.join('  ·  ')

        return (
          <Grid item xs={12} sm={6} md={4} key={session.ratingKey}>
            <Card
              sx={{
                height: '100%',
                position: 'relative',
                display: 'flex',
                flexDirection: 'column',
                overflow: 'hidden',
                transition: 'transform 180ms ease, border-color 180ms ease, box-shadow 180ms ease',
                borderColor: active
                  ? 'rgba(255,115,0,0.6)'
                  : owned ? 'rgba(255,115,0,0.4)' : 'rgba(255,255,255,0.08)',
                // Owned cards get a stronger warm wash — a richer gradient that
                // reads at a glance, plus a faint inset ring so the highlight
                // has structure without competing with the selected state's
                // stronger border + shadow. Selected/active stays dominant.
                background: owned
                  ? 'linear-gradient(180deg, rgba(255,115,0,0.13), rgba(255,115,0,0.02) 68%, rgba(255,115,0,0) 100%)'
                  : undefined,
                boxShadow: active
                  ? '0 14px 40px -22px rgba(255,115,0,0.7)'
                  : owned ? '0 10px 30px -22px rgba(255,115,0,0.45)' : undefined,
                '&:hover': {transform: 'translateY(-2px)', borderColor: 'rgba(255,115,0,0.4)'},
              }}
            >
              <CardActionArea
                onClick={() => onSelect(session)}
                data-owned={owned ? 'true' : undefined}
                aria-label={owned ? `Your session. ${name.top}${name.bottom ? ' ' + name.bottom : ''}` : undefined}
                sx={{
                  display: 'flex', flexDirection: 'column', alignItems: 'stretch', height: '100%',
                  // Explicit, accessible focus ring — the default ripple is hard
                  // to see on the dark background.
                  '&.Mui-focusVisible, &:focus-visible': {
                    outline: '2px solid #ff7300',
                    outlineOffset: '2px',
                  },
                }}
                focusRipple
              >
                {/* Thumbnail — full-width 16:9, with graceful fallback */}
                <Box sx={{position: 'relative', width: '100%', aspectRatio: '16 / 9', background: '#11141a', flexShrink: 0}}>
                  {thumbPath ? (
                    <CardMedia
                      component="img"
                      sx={{position: 'absolute', inset: 0, width: '100%', height: '100%', objectFit: 'cover'}}
                      image={`/thumb?path=${thumbPath}`}
                      alt=""
                    />
                  ) : (
                    <FilmPlaceholder/>
                  )}
                  {/* Player state dot — bottom-left over the thumbnail */}
                  {playerState && (
                    <Box
                      sx={{
                        position: 'absolute', left: 10, bottom: 10, zIndex: 1, pointerEvents: 'none',
                        display: 'inline-flex', alignItems: 'center', gap: 0.75,
                        px: 1, py: 0.4, borderRadius: 999,
                        backgroundColor: 'rgba(13,15,20,0.72)',
                        backdropFilter: 'blur(4px)',
                      }}
                    >
                      <Box sx={{
                        width: 8, height: 8, borderRadius: '50%',
                        backgroundColor: playerState.color,
                        boxShadow: `0 0 8px ${playerState.color}`,
                      }}/>
                      <Typography
                        component="span"
                        sx={{
                          fontSize: '0.66rem', fontWeight: 700, letterSpacing: '0.06em',
                          textTransform: 'uppercase', lineHeight: 1, color: 'rgba(255,255,255,0.92)',
                        }}
                      >
                        {playerState.label}
                      </Typography>
                    </Box>
                  )}
                  {/* Your-session badge — top-right over the thumbnail */}
                  {owned && (
                    <Box
                      sx={{
                        position: 'absolute',
                        top: 10,
                        right: 10,
                        zIndex: 1,
                        pointerEvents: 'none',
                        px: 1.25,
                        py: 0.4,
                        borderRadius: 999,
                        fontSize: '0.7rem',
                        fontWeight: 700,
                        letterSpacing: '0.05em',
                        textTransform: 'uppercase',
                        lineHeight: 1,
                        whiteSpace: 'nowrap',
                        color: '#1a1206',
                        backgroundColor: '#ff7300',
                        boxShadow: '0 4px 14px -4px rgba(255,115,0,0.7)',
                      }}
                    >
                      Your session
                    </Box>
                  )}
                </Box>

                <CardContent sx={{flex: 1, display: 'flex', flexDirection: 'column', gap: 1, p: 2, minWidth: 0}}>
                  {/* Primary + secondary — full card width, one line each */}
                  <Typography noWrap sx={{fontWeight: 700, fontSize: '1.08rem', lineHeight: 1.25}}>
                    {name.top}
                  </Typography>
                  {name.bottom && (
                    <Typography noWrap variant="body2" sx={{color: 'text.secondary', mt: -0.5}}>
                      {name.bottom}
                    </Typography>
                  )}

                  {/* Progress — viewOffset / duration, with times */}
                  {hasProgress && (
                    <Box sx={{mt: 0.5, minWidth: 0}}>
                      <LinearProgress
                        variant="determinate"
                        value={progressPct}
                        aria-hidden
                        sx={{
                          height: 6, borderRadius: 999,
                          backgroundColor: 'rgba(255,255,255,0.08)',
                          '& .MuiLinearProgress-bar': {
                            borderRadius: 999,
                            backgroundColor: '#ff7300',
                          },
                        }}
                      />
                      <Box sx={{display: 'flex', justifyContent: 'space-between', mt: 0.5, gap: 1}}>
                        <Typography variant="caption" sx={{fontFamily: 'var(--cs-mono-font)', color: '#ff7300'}}>
                          {millisToDuration(viewOffset)}
                        </Typography>
                        <Typography variant="caption" sx={{fontFamily: 'var(--cs-mono-font)', color: 'text.disabled'}}>
                          {millisToDuration(duration)}
                        </Typography>
                      </Box>
                    </Box>
                  )}

                  {/* Quality accent — resolution · audio layout, the most
                      decision-useful technical signal (source quality you'll
                      get). Accent-styled so it reads above the footer. */}
                  {quality && (
                    <Box sx={{
                      alignSelf: 'flex-start',
                      mt: 0.5,
                      px: 1.25, py: 0.35,
                      borderRadius: 999,
                      fontSize: '0.72rem',
                      fontWeight: 700,
                      letterSpacing: '0.03em',
                      fontFamily: 'var(--cs-mono-font)',
                      whiteSpace: 'nowrap',
                      color: '#ffd9b0',
                      backgroundColor: 'rgba(255,115,0,0.12)',
                      border: '1px solid rgba(255,115,0,0.32)',
                    }}>
                      {quality}
                    </Box>
                  )}

                  {/* Footer — supporting context (who/where), muted */}
                  {footer && (
                    <Typography noWrap variant="caption" sx={{color: 'text.secondary', mt: 'auto', pt: 0.5}}>
                      {footer}
                    </Typography>
                  )}
                </CardContent>
              </CardActionArea>
            </Card>
          </Grid>
        )
      })}
    </Grid>
  )
}
