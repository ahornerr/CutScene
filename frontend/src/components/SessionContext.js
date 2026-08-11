import {Box, Button, Typography} from "@mui/material";

// Session context bar — makes the active source unmistakable in the workspace.
// Shows the title/context, a small source-kind label (Active session vs. Plex
// library), and a graceful Change-source affordance. The Change button is
// disabled when a render job is active.
function getVideoName(session) {
  if (session.type === "episode") {
    return {
      top: session.grandparentTitle,
      bottom: `S${String(session.parentIndex).padStart(2, '0')}E${String(session.index).padStart(2, '0')} ${session.title}`,
    }
  }
  return {top: session.title, bottom: session.year ? `(${session.year})` : ''}
}

export default function SessionContext({session, onChangeSession, changeDisabled, changeDisabledReason}) {
  const name = getVideoName(session)
  const isLibrary = session?._sourceType === 'library'
  const sourceLabel = isLibrary ? 'From your Plex library' : 'Active Plex session'
  const media = session?.Media?.[0]
  const resolution = media?.videoResolution ? String(media.videoResolution).toUpperCase() : null
  const audioChannels = Number(media?.audioChannels)
  const audio = Number.isFinite(audioChannels) && audioChannels > 0 ? (audioChannels === 6 ? '5.1' : audioChannels === 8 ? '7.1' : audioChannels === 2 ? '2.0' : audioChannels === 1 ? '1.0' : `${audioChannels}ch`) : null
  const codec = media?.videoCodec ? String(media.videoCodec).toUpperCase() : null
  const main10 = String(media?.videoProfile || '').toLowerCase() === 'main 10'
  const audioCodec = media?.audioCodec ? String(media.audioCodec).toUpperCase() : null
  const audioInfo = [audioCodec, audio].filter(Boolean).join(' · ')
  return (
    <Box className="cs-session-context cs-rise">
      <Box sx={{flex: 1, minWidth: 0}}>
        <Typography variant="overline" className="cs-section-label" sx={{color: '#ff7300'}}>
          Now clipping
        </Typography>
        <Typography noWrap sx={{fontWeight: 600, fontSize: '1.15rem', mt: 0.25}}>
          {name.top}
        </Typography>
        {name.bottom && (
          <Typography noWrap variant="body2" sx={{color: 'text.secondary', mt: 0.15}}>
            {name.bottom}
          </Typography>
        )}
        <Typography variant="caption" sx={{color: 'text.disabled', mt: 0.4, display: 'block'}}>
          {sourceLabel}
        </Typography>
        {(resolution || audioInfo || codec || main10) && (
          <Box aria-label="Source media details" sx={{display: 'flex', flexWrap: 'wrap', gap: 0.75, mt: 1}}>
            {resolution && <MediaChip>{resolution}</MediaChip>}
            {audioInfo && <MediaChip>{audioInfo}</MediaChip>}
            {codec && <MediaChip tone="blue">{codec}</MediaChip>}
            {main10 && <MediaChip tone="purple">Main 10</MediaChip>}
          </Box>
        )}
      </Box>
      <Button
        variant="outlined"
        size="small"
        onClick={onChangeSession}
        disabled={changeDisabled}
        title={changeDisabled ? changeDisabledReason : ''}
        sx={{borderColor: 'rgba(255,255,255,0.2)', flexShrink: 0}}
      >
        Change source
      </Button>
    </Box>
  )
}

function MediaChip({children, tone = 'default'}) {
  const colors = {
    default: {color: '#ffd9b0', background: 'rgba(255,115,0,0.12)', border: 'rgba(255,115,0,0.32)'},
    blue: {color: '#cfe3ff', background: 'rgba(73,121,193,0.14)', border: 'rgba(143,184,255,0.32)'},
    purple: {color: '#d9c6ff', background: 'rgba(137,98,201,0.15)', border: 'rgba(185,151,246,0.38)'},
    muted: {color: '#d6dbe5', background: 'rgba(255,255,255,0.06)', border: 'rgba(255,255,255,0.14)'},
  }[tone]
  return <Box sx={{px: 1, py: 0.25, borderRadius: 999, fontSize: '0.68rem', fontWeight: 700, letterSpacing: '0.04em', fontFamily: 'var(--cs-mono-font)', color: colors.color, backgroundColor: colors.background, border: `1px solid ${colors.border}`}}>{children}</Box>
}
