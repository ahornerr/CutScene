import {Box, Paper, Typography} from "@mui/material";
import SessionContext from "./SessionContext";
import PlayerPane from "./PlayerPane";
import TrimScrubber from "./TrimScrubber";
import SubtitlePanel from "./SubtitlePanel";
import RenderJobBar from "./RenderJobBar";

// Two-pane workspace shell. CSS grid (.cs-workspace) reflows to one column
// below 960px — no duplicate DOM trees per breakpoint. Theater mode collapses
// the desktop grid to a single full-width column (preview rail on top,
// subtitles beneath) for an immersive preview, mirroring YouTube theater mode.
export default function ClipWorkspace({
  session, playerUrl, onPlayerError, onPlayerReady,
  previewStale, onApplyPreview,
  startPosition, endPosition,
  onRangeChange, onStartChange, onEndChange,
  trimFlashKey,
  onChangeSession, changeSessionDisabled, changeSessionDisabledReason,
  renderState, jobSpec, controlsChangedSinceJob,
  onCreateJob, onDownloadJob, onRetryJob, onRetryPoll, onCreateNewJob,
  audioMode, onAudioModeChange,
  theaterMode, onToggleTheater,
              subtitleOffsetMs, onSubtitleOffsetChange,
              subtitle, ...subProps
            }) {
  return (
    <>
      <SessionContext
        session={session}
        onChangeSession={onChangeSession}
        changeDisabled={changeSessionDisabled}
        changeDisabledReason={changeSessionDisabledReason}
      />
      <Box className={`cs-workspace${theaterMode ? ' cs-workspace--theater' : ''}`}>
        {/* Left: player + trim + render controls */}
        <Box sx={{display: 'flex', flexDirection: 'column', minWidth: 0}}>
          <PlayerPane
            playerUrl={playerUrl}
            onError={onPlayerError}
            onReady={onPlayerReady}
            previewStale={previewStale}
            onApplyPreview={onApplyPreview}
            theaterMode={theaterMode}
            onToggleTheater={onToggleTheater}
          />
          <TrimScrubber
            duration={session.duration}
            startPosition={startPosition}
            endPosition={endPosition}
            onRangeChange={onRangeChange}
            onStartChange={onStartChange}
            onEndChange={onEndChange}
            trimFlashKey={trimFlashKey}
          />
          {/* Render job / status / controls live directly under the video and
              trim times so the whole left rail reads as the "clip" workflow. */}
          <RenderJobBar
            startPosition={startPosition}
            endPosition={endPosition}
            selectedSubtitle={subtitle}
            audioMode={audioMode}
            onAudioModeChange={onAudioModeChange}
            renderState={renderState}
            jobSpec={jobSpec}
            controlsChangedSinceJob={controlsChangedSinceJob}
            onCreateJob={onCreateJob}
            onDownload={onDownloadJob}
            onRetry={onRetryJob}
            onRetryPoll={onRetryPoll}
            onCreateNew={onCreateNewJob}
          />
        </Box>

        {/* Right: subtitle panel */}
        <Box className="cs-workspace-subtitles" sx={{display: 'flex', flexDirection: 'column', minWidth: 0}}>
          <Paper
            variant="outlined"
            sx={{
              p: 2,
              borderColor: 'rgba(255,255,255,0.08)',
              backgroundColor: 'rgba(0,0,0,0.16)',
              display: 'flex',
              flexDirection: 'column',
              minHeight: {md: 420},
            }}
          >
            <Typography variant="overline" className="cs-section-label" sx={{color: '#ff7300'}}>
              Subtitles
            </Typography>
            <Box sx={{flex: 1, display: 'flex', flexDirection: 'column', minHeight: 0, mt: 0.5}}>
              <SubtitlePanel
                subtitleOffsetMs={subtitleOffsetMs}
                onSubtitleOffsetChange={onSubtitleOffsetChange}
                {...subProps}
              />
            </Box>
          </Paper>
        </Box>
      </Box>
    </>
  )
}
