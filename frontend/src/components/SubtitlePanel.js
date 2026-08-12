import {Box, FormControl, InputLabel, MenuItem, Select, Typography} from "@mui/material";
import SubtitleList from "./SubtitleList";
import SubtitleOffsetControl from "./SubtitleOffsetControl";

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
  const currentCodec = streams.find(s => s.index === selectedSubtitle)?.codec
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
            {streams.map((stream, idx) => (
              <MenuItem key={idx} value={stream.index}>
                {stream.language
                  ? `${stream.language} (${stream.displayTitle || 'Track ' + (stream.index + 1)})`
                  : stream.displayTitle || 'Track ' + (stream.index + 1)}
              </MenuItem>
            ))}
          </Select>
        </FormControl>
        {currentCodec && (
          <Typography variant="caption" sx={{fontFamily: 'var(--cs-mono-font)', color: 'text.secondary'}}>
            {currentCodec}
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
