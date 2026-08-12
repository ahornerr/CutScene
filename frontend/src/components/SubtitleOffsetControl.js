import {Box, IconButton, Tooltip, Typography} from "@mui/material";

// Subtitle offset stepper — a compact, contextual control that lives next to
// the subtitle track selector. Positive values delay subtitles (they appear
// later); negative values advance them (earlier). The value is retained
// across subtitle selections and applied to both the preview URL and the
// render-job request body as `subtitleOffsetMs`.
//
// Interactions:
//   • − / + buttons step by SUBTITLE_OFFSET_STEP_MS (100ms)
//   • clicking the value pill resets to 0
//   • disabled entirely when no subtitle track is selected
//
// The pill uses the workspace orange accent so the active offset reads at a
// glance without crowding the subtitle header.

export const SUBTITLE_OFFSET_STEP_MS = 100
export const SUBTITLE_OFFSET_MIN_MS = -60000
export const SUBTITLE_OFFSET_MAX_MS = 60000

export function clampSubtitleOffsetMs(value) {
  const n = Number(value)
  if (!Number.isFinite(n)) return 0
  const rounded = Math.round(n)
  return Math.max(SUBTITLE_OFFSET_MIN_MS, Math.min(SUBTITLE_OFFSET_MAX_MS, rounded))
}

export function formatSubtitleOffsetMs(ms) {
  const n = clampSubtitleOffsetMs(ms)
  if (n === 0) return '0 ms'
  const sign = n > 0 ? '+' : '−' // U+2212 minus for visual symmetry with +
  return `${sign}${Math.abs(n)} ms`
}

// Inline SVG glyphs — stays consistent with SubtitleList's hand-rolled icons
// and avoids pulling in @mui/icons-material.
function MinusIcon(props) {
  return (
    <svg viewBox="0 0 24 24" width="1em" height="1em" fill="none" stroke="currentColor"
      strokeWidth="2.4" strokeLinecap="round" {...props}>
      <path d="M5 12h14"/>
    </svg>
  )
}
function PlusIcon(props) {
  return (
    <svg viewBox="0 0 24 24" width="1em" height="1em" fill="none" stroke="currentColor"
      strokeWidth="2.4" strokeLinecap="round" {...props}>
      <path d="M12 5v14M5 12h14"/>
    </svg>
  )
}

export default function SubtitleOffsetControl({value, onChange, disabled}) {
  const current = clampSubtitleOffsetMs(value)
  const isNonZero = current !== 0
  const step = (dir) => {
    if (disabled) return
    onChange(clampSubtitleOffsetMs(current + dir * SUBTITLE_OFFSET_STEP_MS))
  }
  const reset = () => {
    if (disabled) return
    if (current !== 0) onChange(0)
  }

  return (
    <Tooltip
      title="Shift subtitles earlier (−) or later (+). Applied to preview and render. Click the value to reset."
      placement="bottom"
    >
      <Box
        component="span"
        sx={{
          display: 'inline-flex',
          alignItems: 'center',
          gap: 0.5,
          // Subtle bordered pill keeps the stepper compact and visually
          // distinct from the track Select while sharing the same row.
          px: 0.5,
          py: 0.25,
          borderRadius: 999,
          border: '1px solid',
          borderColor: isNonZero && !disabled ? 'rgba(255,115,0,0.4)' : 'rgba(255,255,255,0.12)',
          backgroundColor: isNonZero && !disabled ? 'rgba(255,115,0,0.08)' : 'transparent',
          transition: 'border-color 160ms ease, background-color 160ms ease',
          opacity: disabled ? 0.5 : 1,
        }}
      >
        <Typography
          variant="caption"
          sx={{color: 'text.secondary', px: 0.25, fontWeight: 600, letterSpacing: '0.04em'}}
        >
          Offset
        </Typography>
        <IconButton
          size="small"
          aria-label="Subtitles earlier"
          title="Earlier"
          disabled={disabled}
          onClick={() => step(-1)}
          sx={{
            p: {xs: 1, sm: 0.25},
            color: 'text.secondary',
            '&:hover': {color: '#ff7300', backgroundColor: 'rgba(255,115,0,0.12)'},
          }}
        >
          <MinusIcon sx={{fontSize: 16}}/>
        </IconButton>
        <Box
          component="button"
          type="button"
          aria-label="Reset subtitle offset"
          disabled={disabled || !isNonZero}
          onClick={reset}
          sx={{
            border: 0,
            background: 'transparent',
            px: 0.75,
            py: {xs: 1, sm: 0.25},
            borderRadius: 999,
            cursor: disabled || !isNonZero ? 'default' : 'pointer',
            font: 'inherit',
            fontFamily: 'var(--cs-mono-font)',
            fontSize: '0.78rem',
            fontWeight: 600,
            minWidth: {xs: 72, sm: 58},
            minHeight: {xs: 40, sm: 0},
            textAlign: 'center',
            color: isNonZero && !disabled ? '#ffd9b0' : 'text.secondary',
            '&:hover': disabled || !isNonZero ? {} : {backgroundColor: 'rgba(255,115,0,0.14)'},
          }}
        >
          {formatSubtitleOffsetMs(current)}
        </Box>
        <IconButton
          size="small"
          aria-label="Subtitles later"
          title="Later"
          disabled={disabled}
          onClick={() => step(1)}
          sx={{
            p: {xs: 1, sm: 0.25},
            color: 'text.secondary',
            '&:hover': {color: '#ff7300', backgroundColor: 'rgba(255,115,0,0.12)'},
          }}
        >
          <PlusIcon sx={{fontSize: 16}}/>
        </IconButton>
      </Box>
    </Tooltip>
  )
}
