import {Box, Button, Tooltip, Typography} from "@mui/material";
import ReactPlayer from "react-player";

// Theater-mode glyph — a widescreen rectangle with side bars. Inline SVG keeps
// the control dependency-free, matching SubtitleOffsetControl's hand-rolled
// icons. Filled variant reads as "active" when theater mode is on.
function TheaterIcon({active, ...props}) {
  return (
    <svg viewBox="0 0 24 24" width="1em" height="1em" fill="none"
      stroke="currentColor" strokeWidth="2" strokeLinecap="round"
      strokeLinejoin="round" {...props}>
      <rect x="2.5" y="6" width="19" height="12" rx="2.5"/>
      {active
        ? <path d="M6 9.5h12v5H6z" fill="currentColor" stroke="none"/>
        : <path d="M6 9.5h12v5H6z"/>}
    </svg>
  )
}

// Player pane. Receives the preview URL and surfaces load errors via onError.
// Responsive via CSS aspect-ratio container — no duplicate DOM per breakpoint.
// When previewStale is true, shows a "Preview selection" button overlay so the
// user controls when the player reloads, preventing jank during trim editing.
//
// Theater mode is a desktop-only view toggle (mobile is already single-column).
// It widens the preview rail and drops the subtitle panel beneath it; the
// toggle sits in a thin toolbar above the player frame so it never collides
// with the stale-preview overlay. aria-pressed carries the on/off state for
// assistive tech while the visible label flips between Theater/Default.
export default function PlayerPane({
  playerUrl, onError, onReady, previewStale, onApplyPreview,
  theaterMode, onToggleTheater,
}) {
  const theaterOn = !!theaterMode
  return (
    <Box className="cs-rise">
      <Box
        className="cs-player-toolbar"
        sx={{display: {xs: 'none', md: 'flex'}, justifyContent: 'flex-end', mb: 1, gap: 1}}
      >
        <Tooltip
          title={theaterOn
            ? 'Exit theater mode — show subtitles beside the preview'
            : 'Enter theater mode — widen the preview and move subtitles below'}
          placement="bottom"
        >
          <Button
            size="small"
            variant="text"
            onClick={onToggleTheater}
            aria-label="Theater mode"
            aria-pressed={theaterOn ? 'true' : 'false'}
            className="cs-theater-toggle"
            startIcon={<TheaterIcon active={theaterOn} sx={{fontSize: 18}}/>}
            sx={{
              color: theaterOn ? '#ffd9b0' : 'text.secondary',
              px: 1.25,
              py: 0.25,
              borderRadius: 999,
              border: '1px solid',
              borderColor: theaterOn ? 'rgba(255,115,0,0.45)' : 'rgba(255,255,255,0.12)',
              backgroundColor: theaterOn ? 'rgba(255,115,0,0.10)' : 'transparent',
              transition: 'border-color 160ms ease, background-color 160ms ease, color 160ms ease',
              '&:hover': {
                color: '#ffd9b0',
                borderColor: 'rgba(255,115,0,0.45)',
                backgroundColor: 'rgba(255,115,0,0.10)',
              },
              '&.Mui-focusVisible': {outline: '2px solid #ffd9b0', outlineOffset: 2},
            }}
          >
            {theaterOn ? 'Default view' : 'Theater mode'}
          </Button>
        </Tooltip>
      </Box>
      <Box className="cs-player-frame" data-stale={previewStale ? 'true' : undefined}>
        {playerUrl ? (
          <ReactPlayer
            url={playerUrl}
            controls
            playing
            width="100%"
            height="100%"
            onError={onError}
            onReady={onReady}
            config={{file: {attributes: {preload: "auto"}}}}
          />
        ) : (
          <Box sx={{position: 'absolute', inset: 0, display: 'grid', placeItems: 'center'}}>
            <Typography variant="body2" sx={{color: 'text.secondary'}}>Preparing preview…</Typography>
          </Box>
        )}
        {previewStale && (
          <Button
            size="small"
            variant="contained"
            color="primary"
            onClick={onApplyPreview}
            className="cs-preview-btn"
          >
            Preview selection
          </Button>
        )}
      </Box>
    </Box>
  )
}