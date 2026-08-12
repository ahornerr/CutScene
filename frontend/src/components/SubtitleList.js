import {Box, IconButton, InputAdornment, TextField, Typography} from "@mui/material";
import StateMessage from "./StateMessage";
import {millisToDuration} from "../utils";
import {renderSubtitleMarkup, stripSubtitleMarkup} from "./subtitle-markup";

// Inline SVG icons — avoids adding @mui/icons-material as a dependency.
function SearchIcon(props) {
  return (
    <svg viewBox="0 0 24 24" width="1em" height="1em" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" {...props}>
      <circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/>
    </svg>
  )
}
function ClearIcon(props) {
  return (
    <svg viewBox="0 0 24 24" width="1em" height="1em" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" {...props}>
      <path d="M18 6 6 18M6 6l12 12"/>
    </svg>
  )
}

// Subtitle list with search. Rows are focusable, keyboard-activatable buttons
// that support single selection (Enter/Space/click) and shift-range selection
// (Shift+click or Shift+Enter) in full-list mode.
export default function SubtitleList({
  mode, entries, totalCount, anchor, selectionRange,
  onPick, search, onSearchChange, onClearSearch,
  loading, error, listRef,
}) {
  const isFull = mode === 'full'

  return (
    <Box sx={{display: 'flex', flexDirection: 'column', minHeight: 0, flex: 1}}>
      <TextField
        fullWidth
        variant="outlined"
        size="small"
        placeholder="Search subtitles…"
        label="Search subtitles"
        value={search}
        onChange={e => onSearchChange(e.target.value)}
        InputProps={{
          startAdornment: <InputAdornment position="start"><SearchIcon sx={{color: 'text.secondary', fontSize: 18}}/></InputAdornment>,
          endAdornment: search ? (
            <InputAdornment position="end">
              <IconButton
                aria-label="Clear search"
                size="small"
                edge="end"
                onClick={onClearSearch}
              >
                <ClearIcon sx={{fontSize: 18}}/>
              </IconButton>
            </InputAdornment>
          ) : null,
        }}
        sx={{mt: 1.25}}
      />

      <Box
        aria-live="polite"
        aria-busy={loading || undefined}
        className="cs-scroll"
        ref={listRef}
        sx={{
          mt: 1, flex: 1, minHeight: {xs: 280, sm: 240}, maxHeight: {xs: '48vh', md: '42vh'},
          overflowY: 'auto',
          border: '1px solid rgba(255,255,255,0.08)',
          borderRadius: 1.5,
          backgroundColor: 'rgba(0,0,0,0.18)',
        }}
      >
        {loading ? (
          <StateMessage variant="loading" title="Loading subtitles…" live/>
        ) : error ? (
          <StateMessage variant="error" title="Couldn’t load subtitles for this track." live/>
        ) : entries.length === 0 ? (
          <StateMessage
            variant="empty"
            title={isFull ? 'No subtitle entries.' : 'No matching subtitles.'}
            hint={isFull ? undefined : 'Try a different search term.'}
          />
        ) : (
          entries.map((entry, displayIdx) => {
            const fullIdx = isFull ? displayIdx : entry._fullIdx
            const selected = isFull && fullIdx >= selectionRange.from && fullIdx <= selectionRange.to
            return (
              <SubtitleRow
                key={fullIdx}
                entry={entry}
                selected={selected}
                isAnchor={isFull && fullIdx === anchor}
                fullIdx={fullIdx}
                onClick={e => onPick(fullIdx, e.shiftKey)}
                onKeyDown={e => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    if (e.shiftKey && isFull) {
                      e.preventDefault()
                      onPick(fullIdx, true)
                    }
                    // non-shift: let native activation fire onClick
                  } else if (e.key === 'Escape' && !isFull) {
                    onClearSearch()
                  }
                }}
              />
            )
          })
        )}
      </Box>

      <Typography variant="caption" sx={{color: 'text.disabled', mt: 0.75, px: 0.5}}>
        {isFull
          ? `${totalCount} entries · Enter or click sets clip range · Shift+Enter extends`
          : `${entries.length} of ${totalCount} matching`}
      </Typography>
    </Box>
  )
}

function SubtitleRow({entry, selected, isAnchor, fullIdx, onClick, onKeyDown}) {
  return (
    <Box
      component="button"
      type="button"
      onClick={onClick}
      onKeyDown={onKeyDown}
      data-idx={fullIdx}
      aria-pressed={selected}
      aria-label={`Subtitle at ${millisToDuration(entry.start)}: ${stripSubtitleMarkup(entry.text)}${selected ? ', selected' : ''}`}
      className="cs-focusable"
      sx={{
        display: 'flex',
        alignItems: 'flex-start',
        gap: 1.25,
        width: '100%',
        textAlign: 'left',
        px: {xs: 1.25, sm: 1.5},
        py: {xs: 1.1, sm: 0.85},
        cursor: 'pointer',
        backgroundColor: selected ? 'rgba(255,115,0,0.16)' : 'transparent',
        border: 0,
        borderLeft: isAnchor ? '3px solid #ff7300' : '3px solid transparent',
        borderBottom: '1px solid rgba(255,255,255,0.05)',
        color: 'inherit',
        font: 'inherit',
        transition: 'background-color 120ms ease',
        '&:hover': {backgroundColor: selected ? 'rgba(255,115,0,0.24)' : 'rgba(255,255,255,0.05)'},
      }}
    >
      <Typography
        variant="caption"
        sx={{minWidth: {xs: 68, sm: 74}, flexShrink: 0, fontFamily: 'var(--cs-mono-font)', color: selected ? '#ffd9b0' : 'text.secondary', pt: '1px'}}
      >
        {millisToDuration(entry.start)}
      </Typography>
      <Typography variant="body2" sx={{flex: 1, whiteSpace: 'pre-wrap'}}>
        {renderSubtitleMarkup(entry.text)}
      </Typography>
    </Box>
  )
}
