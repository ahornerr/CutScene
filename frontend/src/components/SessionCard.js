import {Box, Card, CardActionArea, CardContent, CardMedia, Grid, LinearProgress, Typography} from "@mui/material";

// A single active-session source card. Extracted from SessionPicker so the
// unified source picker can render session cards in the same results region as
// library cards without duplicating the markup. The visual language is
// preserved exactly — "Your session" badge, player-state dot, progress bar,
// quality pill, muted footer — so active-session sources keep their identity
// inside the unified picker.

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

function getPlayerState(state) {
  switch (state) {
    case 'playing': return {color: '#7fd391', label: 'Playing'}
    case 'paused': return {color: '#ffd9a0', label: 'Paused'}
    case 'buffering': return {color: '#8fb8ff', label: 'Buffering'}
    default: return null
  }
}

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

function resolutionLabel(res) {
  if (!res) return null
  const s = String(res).trim()
  return s ? s.toUpperCase() : null
}

function qualitySummary(res, audio) {
  const parts = [res, audio].filter(Boolean)
  return parts.length ? parts.join(' · ') : null
}

function locationLabel(loc) {
  if (loc === 'lan') return 'Local'
  if (loc === 'wan') return 'Remote'
  return null
}

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

export default function SessionCard({session, active, onSelect}) {
  const name = getVideoName(session)
  const owned = session.ownedByCurrentUser === true

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
  const thumbPath = session.thumb || session.grandparentThumb || null

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
            '&.Mui-focusVisible, &:focus-visible': {
              outline: '2px solid #ff7300',
              outlineOffset: '2px',
            },
          }}
          focusRipple
        >
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
            <Typography noWrap sx={{fontWeight: 700, fontSize: '1.08rem', lineHeight: 1.25}}>
              {name.top}
            </Typography>
            {name.bottom && (
              <Typography noWrap variant="body2" sx={{color: 'text.secondary', mt: -0.5}}>
                {name.bottom}
              </Typography>
            )}

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
}