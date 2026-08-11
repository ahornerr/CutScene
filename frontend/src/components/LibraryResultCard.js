import {Box, Card, CardActionArea, CardContent, CardMedia, Grid, Typography} from "@mui/material";
import {millisToDuration} from "../utils";

// Plex library search result card. Mirrors the SessionPicker card's visual
// language (16:9 artwork, title + context, quality pill, muted footer) so the
// two source kinds read as peers. Differences are intentional and signal the
// source type rather than a live session:
//   • a "Library" badge instead of "Your session"
//   • no progress bar (library items have no viewOffset)
//   • a footer that names the media type and total runtime
//
// `result` follows the backend LibrarySearchResult contract: ratingKey,
// mediaId, partId, title, type, year, grandparentTitle, parentTitle,
// seasonNumber, episodeNumber, duration, artwork, videoResolution, audioChannels.

function getResultName(result) {
  if (result.type === 'episode') {
    // Show title falls back through grandparent → parent → episode title so a
    // sparse hub result still has a recognizable primary line.
    const top = result.grandparentTitle || result.parentTitle || result.title || ''
    // Only compose the S##E## line when at least one number is present —
    // rendering "SnullEnull" for a result missing both is worse than omitting
    // the line. When only one number exists, show the available one.
    const hasSeason = result.seasonNumber != null
    const hasEpisode = result.episodeNumber != null
    let bottom = ''
    if (hasSeason || hasEpisode) {
      const season = hasSeason ? String(result.seasonNumber).padStart(2, '0') : ''
      const episode = hasEpisode ? String(result.episodeNumber).padStart(2, '0') : ''
      const code = `S${season}E${episode}`
      bottom = result.title ? `${code} ${result.title}` : code
    } else if (result.title && result.title !== top) {
      bottom = result.title
    }
    return {top, bottom}
  }
  return {top: result.title, bottom: result.year ? `(${result.year})` : ''}
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

function typeLabel(type) {
  if (type === 'movie') return 'Movie'
  if (type === 'episode') return 'Episode'
  if (type === 'show') return 'Show'
  if (type === 'season') return 'Season'
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

export default function LibraryResultCard({result, active, onSelect, onNavigate}) {
  const name = getResultName(result)
  const res = resolutionLabel(result.videoResolution)
  const audio = audioLayoutLabel(result.audioChannels)
  const quality = qualitySummary(res, null)
  const codec = result.videoCodec ? String(result.videoCodec).toUpperCase() : null
  const main10 = String(result.videoProfile || '').toLowerCase() === 'main 10'
  const audioInfo = [result.audioCodec ? String(result.audioCodec).toUpperCase() : null, audio].filter(Boolean).join(' · ')
  const tLabel = typeLabel(result.type)
  const navigation = result.type === 'show' || result.type === 'season'
  const selectable = result.type === 'movie' || result.type === 'episode'
  const duration = result.duration
  const thumbPath = result.artwork || null

  // Footer — media type, total runtime. Muted like the session card footer.
  const footerParts = []
  if (tLabel) footerParts.push(tLabel)
  if (duration > 0) footerParts.push(millisToDuration(duration))
  const footer = footerParts.join('  ·  ')

  return (
    <Grid item xs={12} sm={6} md={4} key={result.ratingKey}>
      <Card
        sx={{
          height: '100%',
          position: 'relative',
          display: 'flex',
          flexDirection: 'column',
          overflow: 'hidden',
          transition: 'transform 180ms ease, border-color 180ms ease, box-shadow 180ms ease',
          // Library cards use a cooler accent than the warm "Your session"
          // wash — they're a peer source kind, not the same kind. Selected
          // keeps the dominant orange border + shadow so the active source is
          // always unmistakable regardless of how it was picked.
          borderColor: active
            ? 'rgba(255,115,0,0.6)'
            : 'rgba(255,255,255,0.08)',
          background: active
            ? 'linear-gradient(180deg, rgba(255,115,0,0.10), rgba(255,115,0,0.02) 68%, rgba(255,115,0,0) 100%)'
            : undefined,
          boxShadow: active ? '0 14px 40px -22px rgba(255,115,0,0.7)' : undefined,
          '&:hover': {transform: 'translateY(-2px)', borderColor: 'rgba(255,115,0,0.4)'},
        }}
      >
        <CardActionArea
          onClick={() => navigation ? onNavigate?.(result) : selectable ? onSelect(result) : undefined}
          disabled={!navigation && !selectable}
          aria-label={`${navigation ? `Browse ${result.type === 'show' ? 'seasons' : 'episodes'}` : selectable ? 'Library result' : 'Unavailable library result'}. ${name.top}${name.bottom ? ' ' + name.bottom : ''}`}
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
                image={`/thumb?path=${encodeURIComponent(thumbPath)}`}
                alt=""
              />
            ) : (
              <FilmPlaceholder/>
            )}
            {/* Library badge — top-right over the thumbnail. A cooler outline
                pill distinguishes library sources from the warm filled "Your
                session" badge on active-session cards. */}
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
                color: '#cfe3ff',
                backgroundColor: 'rgba(28,42,76,0.78)',
                border: '1px solid rgba(143,184,255,0.35)',
                backdropFilter: 'blur(4px)',
              }}
            >
              {navigation ? 'Browse' : 'Library'}
            </Box>
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

            {(quality || audioInfo || codec || main10) && (
              <Box sx={{display: 'flex', flexWrap: 'wrap', gap: 0.75, mt: 0.5}}>
              {quality && <Box sx={{
                alignSelf: 'flex-start',
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
              </Box>}
              {codec && <Box sx={{px: 1.25, py: 0.35, borderRadius: 999, fontSize: '0.72rem', fontWeight: 700,
                letterSpacing: '0.03em', fontFamily: 'var(--cs-mono-font)', color: '#cfe3ff',
                backgroundColor: 'rgba(73,121,193,0.14)', border: '1px solid rgba(143,184,255,0.32)'}}>{codec}</Box>}
              {audioInfo && <Box sx={{px: 1.25, py: 0.35, borderRadius: 999, fontSize: '0.72rem', fontWeight: 700,
                letterSpacing: '0.03em', fontFamily: 'var(--cs-mono-font)', color: '#d6dbe5',
                backgroundColor: 'rgba(255,255,255,0.06)', border: '1px solid rgba(255,255,255,0.14)'}}>{audioInfo}</Box>}
              {main10 && <Box sx={{px: 1.25, py: 0.35, borderRadius: 999, fontSize: '0.72rem', fontWeight: 700,
                letterSpacing: '0.03em', fontFamily: 'var(--cs-mono-font)', color: '#d9c6ff',
                backgroundColor: 'rgba(137,98,201,0.15)', border: '1px solid rgba(185,151,246,0.38)'}}>Main 10</Box>}
              </Box>
            )}

            {footer && (
              <Typography noWrap variant="caption" sx={{color: 'text.secondary', mt: 'auto', pt: 0.5}}>
                {footer}
              </Typography>
            )}
            {navigation && (
              <Typography variant="caption" sx={{color: '#cfe3ff', fontWeight: 700, mt: 'auto', pt: 0.5}}>
                {result.type === 'show' ? 'Browse seasons' : 'Browse episodes'}
              </Typography>
            )}
          </CardContent>
        </CardActionArea>
      </Card>
    </Grid>
  )
}
