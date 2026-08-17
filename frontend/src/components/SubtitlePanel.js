import {Box, FormControl, InputLabel, MenuItem, Select, Typography} from "@mui/material";
import SubtitleList from "./SubtitleList";
import SubtitleOffsetControl from "./SubtitleOffsetControl";
import {orderSubtitleStreams, subtitleFormat} from "./subtitle-formats";

// Right-rail subtitle control room: track selector on top, searchable list below.
// The subtitle offset stepper lives beside the track selector so all subtitle
// timing controls sit together; the offset is retained across subtitle
// selections and applied to both preview and render requests.
export default function SubtitlePanel({
  streams, selectedSubtitle, onSubtitleChange, streamsLoading, streamsError,
  subtitleOffsetMs, onSubtitleOffsetChange,
  listMode, listEntries, totalCount, anchor, selectionRange,
  onPick, search, onSearchChange, onClearSearch,
  loading, error, listRef,
}) {
  const hasStreams = streams.length > 0
  const orderedStreams = orderSubtitleStreams(streams)
  const currentStream = streams.find(s => s.index === selectedSubtitle)
  const currentFormat = currentStream ? subtitleFormat(currentStream).label : null
  const offsetDisabled = selectedSubtitle < 0

  return (
    <Box className="cs-rise-3" sx={{display: 'flex', flexDirection: 'column', minHeight: 0, height: '100%'}}>
      <Box sx={{
        display: 'flex', alignItems: 'center', gap: {xs: 1, sm: 1.5}, flexWrap: 'wrap',
        // Slightly taller bar gives the track selector + offset stepper more
        // presence as a distinct subtitle control bar.
        py: 0.75, minHeight: 52,
      }}>
        <FormControl fullWidth size="small" sx={{maxWidth: {xs: 'none', sm: 280}}}>
          <InputLabel id="subtitle-select">Subtitle track</InputLabel>
          <Select
            labelId="subtitle-select"
            value={selectedSubtitle}
            label="Subtitle track"
            onChange={e => onSubtitleChange(e.target.value)}
          >
            <MenuItem value={-1}>None</MenuItem>
            {orderedStreams.map((stream, idx) => {
              const format = subtitleFormat(stream).label
              const title = stream.language
                ? `${stream.language} (${stream.displayTitle || 'Track ' + (stream.index + 1)})`
                : stream.displayTitle || 'Track ' + (stream.index + 1)
              const formatId = `subtitle-format-${stream.index}`
              return <MenuItem key={idx} value={stream.index} aria-label={title} aria-describedby={formatId}>
                <Box sx={{display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 1.5, width: '100%'}}>
                  <span>{title}</span>
                  <Box component="span" aria-hidden="true" sx={{flexShrink: 0, px: .75, py: .1, borderRadius: 99, fontSize: '.68rem', fontWeight: 700, letterSpacing: '.04em', color: '#ffd9b0', backgroundColor: 'rgba(255,115,0,.12)', border: '1px solid rgba(255,115,0,.25)'}}>{format}</Box>
                  <Box id={formatId} component="span" sx={{position: 'absolute', width: 1, height: 1, p: 0, m: -1, overflow: 'hidden', clip: 'rect(0 0 0 0)', whiteSpace: 'nowrap', border: 0}}>Format: {format}</Box>
                </Box>
              </MenuItem>
            })}
          </Select>
        </FormControl>
        {currentFormat && (
          <Typography variant="caption" sx={{fontFamily: 'var(--cs-mono-font)', color: 'text.secondary'}}>
            {currentFormat}
          </Typography>
        )}
        <SubtitleOffsetControl
          value={subtitleOffsetMs}
          onChange={onSubtitleOffsetChange}
          disabled={offsetDisabled}
        />
      </Box>

      {streamsError ? (
        <Typography variant="body2" sx={{color: '#ffb4a8', mt: 1.5}} role="alert">
          Couldn’t load subtitle tracks.
        </Typography>
      ) : streamsLoading ? (
        <Typography variant="body2" sx={{color: 'text.secondary', mt: 2}} aria-live="polite">
          Loading subtitle tracks…
        </Typography>
      ) : selectedSubtitle < 0 ? (
        <Typography variant="body2" sx={{color: 'text.secondary', mt: 2}} aria-live="polite">
          {hasStreams ? 'No track selected.' : 'No subtitle tracks available for this session.'}
        </Typography>
      ) : (
        <SubtitleList
          mode={listMode}
          entries={listEntries}
          totalCount={totalCount}
          anchor={anchor}
          selectionRange={selectionRange}
          onPick={onPick}
          search={search}
          onSearchChange={onSearchChange}
          onClearSearch={onClearSearch}
          loading={loading}
          error={error}
          listRef={listRef}
        />
      )}
    </Box>
  )
}
