import {Box, Button, Chip, CircularProgress, FormControl, InputLabel, MenuItem, Select, ToggleButton, ToggleButtonGroup, Typography} from "@mui/material";
import {millisToDuration} from "../utils";
import {JOB_STATES, jobErrorMessage, jobStatusLabel, formatExpiry, formatJobSpec, isTransportError, AUDIO_MODE_CHOICES, getAvailableResolutionChoices} from "./render-jobs";

// Render-job bar. Displays the immutable submitted spec so the user always
// knows what the current output corresponds to. Separates transport/polling
// failures from render failures. Includes an Audio mode select that feeds
// the preview URL and render-request builders.
//
// States: idle → queued → running → succeeded (download) | failed (retry) | expired
//
// Sticky on mobile, inline on desktop (handled via .cs-sticky-download CSS).
export default function RenderJobBar({
  startPosition, endPosition, selectedSubtitle,
  audioMode, onAudioModeChange, resolution, onResolutionChange, sourceMediaHeight,
  renderState, jobSpec, controlsChangedSinceJob,
  onCreateJob, onDownload, onRetry, onRetryPoll, onCreateNew,
  onOpenClip,
}) {
  const {status, error, downloadUrl, expiresAt, retryAfter, clipId} = renderState
  // A successful render is promoted to a durable saved clip (see clips.go).
  // The terminal job response carries clipId; we surface a discoverable
  // "Open in library" affordance that is visually distinct from the transient
  // download (which expires after one hour). The transient download stays the
  // primary quick-grab action; the saved clip is the durable artifact.
  const savedToLibrary = Boolean(clipId) && (status === JOB_STATES.SUCCEEDED || status === JOB_STATES.EXPIRED)
  const spec = formatJobSpec(jobSpec)
  const ready = startPosition != null && endPosition != null && endPosition - startPosition >= 500
  const busy = status === JOB_STATES.QUEUED || status === JOB_STATES.RUNNING
  const hasJob = status != null
  const showSpec = hasJob && spec
  const showNewRender = hasJob && !busy && controlsChangedSinceJob
  const isRenderFailure = status === JOB_STATES.FAILED && error && !isTransportError(error)
  const isTransportFailure = status === JOB_STATES.FAILED && error && isTransportError(error)
  const resolutionChoices = getAvailableResolutionChoices(sourceMediaHeight)
  const nativeOnly = sourceMediaHeight == null || sourceMediaHeight < 480

  return (
    <Box className="cs-sticky-download" sx={{px: {xs: 1.5, sm: 2, md: 0}, py: {xs: 1.25, md: 0}, mt: {xs: 2.5, md: 2}}}>
      {/* Submitted spec summary — immutable, shown whenever a job exists */}
      {showSpec && (
        <Box sx={{
          mb: 1.5, pb: 1.5, borderBottom: '1px solid rgba(255,255,255,0.06)',
          display: 'flex', flexWrap: 'wrap', gap: 2, alignItems: 'center',
        }}>
          <Box sx={{flex: '1 1 auto', minWidth: 0}}>
            <Typography variant="overline" sx={{color: 'text.secondary', display: 'block', lineHeight: 1}}>
              Submitted clip
            </Typography>
            <Typography sx={{fontFamily: 'var(--cs-mono-font)', fontWeight: 600, fontSize: '0.95rem', mt: 0.25}}>
              {spec.range}
            </Typography>
          </Box>
          <Chip size="small" label={spec.duration} variant="outlined"
            sx={{borderColor: 'rgba(255,255,255,0.18)', color: 'text.secondary', fontFamily: 'var(--cs-mono-font)'}}/>
          <Chip size="small" label={spec.subtitle} variant="outlined"
            sx={{borderColor: 'rgba(255,255,255,0.18)', color: 'text.secondary'}}/>
          <Chip size="small" label={spec.quality} variant="outlined"
            sx={{borderColor: 'rgba(255,255,255,0.18)', color: 'text.secondary'}}/>
          <Chip size="small" label={spec.audioMode} variant="outlined"
            sx={{borderColor: 'rgba(255,255,255,0.18)', color: 'text.secondary'}}/>
          {spec.subtitleOffsetMs !== 0 && (
            <Chip size="small" label={spec.subtitleOffsetLabel} variant="outlined"
              sx={{
                borderColor: 'rgba(255,115,0,0.4)',
                color: '#ffd9b0',
                fontFamily: 'var(--cs-mono-font)',
              }}/>
          )}
        </Box>
      )}

      {/* Output controls — always visible alongside the render controls */}
      <Box sx={{display: 'flex', alignItems: 'flex-end', gap: {xs: 1, sm: 2}, flexWrap: 'wrap', mb: 1.25}}>
        <Box sx={{flex: {xs: '1 1 100%', md: '1 1 100%'}, minWidth: 0}}>
          <Typography id="resolution-select-label" variant="overline" sx={{color: 'text.secondary', display: 'block', lineHeight: 1, mb: 0.8}}>
            Quality
          </Typography>
          <ToggleButtonGroup
            value={resolution}
            exclusive
            onChange={(_, value) => value && onResolutionChange(value)}
            aria-labelledby="resolution-select-label"
            size="small"
            sx={{
              // A two-up grid on phones gives the tier names enough room to
              // remain scannable; larger screens retain the compact four-up rail.
              display: 'grid', gridTemplateColumns: {xs: `repeat(${Math.min(2, resolutionChoices.length)}, minmax(0, 1fr))`, sm: `repeat(${resolutionChoices.length}, minmax(0, 1fr))`}, width: '100%',
              border: '1px solid rgba(255,255,255,0.1)', borderRadius: 2, overflow: 'hidden',
              '& .MuiToggleButtonGroup-grouped': {border: 0, borderRadius: '0 !important', minHeight: {xs: 48, sm: 54}},
              '& .MuiToggleButtonGroup-grouped + .MuiToggleButtonGroup-grouped': {borderLeft: '1px solid rgba(255,255,255,0.08)'},
              '& .MuiToggleButtonGroup-grouped:nth-of-type(odd)': {borderLeft: {xs: '0 !important', sm: undefined}},
              '& .MuiToggleButtonGroup-grouped:nth-of-type(n + 3)': {borderTop: {xs: '1px solid rgba(255,255,255,0.08)', sm: 0}},
              '& .Mui-selected': {backgroundColor: 'rgba(255,115,0,0.18) !important', color: '#ffd9b0', boxShadow: 'inset 0 -2px 0 #ff7300'},
            }}
          >
            {resolutionChoices.map(choice => (
              <ToggleButton key={choice.value} value={choice.value} aria-label={`${nativeOnly && choice.value === 'native' ? 'Native' : choice.label} quality`} sx={{textTransform: 'none', px: {xs: 1, sm: 0.5}}}>
                <Box sx={{lineHeight: 1.05}}>
                  <Typography component="span" sx={{display: 'block', fontSize: '0.82rem', fontWeight: 700}}>{nativeOnly && choice.value === 'native' ? 'Native' : choice.label}</Typography>
                  <Typography component="span" variant="caption" sx={{display: {xs: 'none', sm: 'block'}, color: 'text.secondary', fontSize: '0.64rem'}}>{nativeOnly && choice.value === 'native' ? 'Source' : choice.detail}</Typography>
                </Box>
              </ToggleButton>
            ))}
          </ToggleButtonGroup>
          <Typography variant="caption" sx={{color: 'text.disabled', display: 'block', mt: 0.6}}>
            {nativeOnly
              ? (sourceMediaHeight == null ? 'Source dimensions unavailable — native output only.' : 'Source is below 480p — native output only.')
              : 'Choose the output quality for your clip without upscaling.'}
          </Typography>
        </Box>
        <FormControl size="small" sx={{flex: {xs: '1 1 100%', sm: '1 1 200px'}, minWidth: 0}}>
          <InputLabel id="audio-mode-select">Audio mode</InputLabel>
          <Select
            labelId="audio-mode-select"
            value={audioMode}
            label="Audio mode"
            onChange={e => onAudioModeChange(e.target.value)}
          >
            {AUDIO_MODE_CHOICES.map(choice => (
              <MenuItem key={choice.value} value={choice.value}>
                <Box>
                  <Typography component="span" sx={{fontWeight: 600, display: 'block'}}>
                    {choice.label}
                  </Typography>
                  <Typography component="span" variant="caption" sx={{color: 'text.secondary', display: 'block'}}>
                    {choice.description}
                  </Typography>
                </Box>
              </MenuItem>
            ))}
          </Select>
        </FormControl>
      </Box>

      <Box sx={{display: 'flex', alignItems: 'center', gap: {xs: 1, sm: 2}, flexWrap: 'wrap', mb: {xs: 1.25, md: 0}}}>
        {/* Current clip length (live controls) */}
        <Box sx={{flex: '1 1 auto', minWidth: 0}}>
          <Typography variant="overline" sx={{color: 'text.secondary', display: 'block', lineHeight: 1}}>
            {hasJob ? 'Current selection' : 'Clip length'}
          </Typography>
          <Typography sx={{fontFamily: 'var(--cs-mono-font)', fontWeight: 600, fontSize: '1.05rem'}}>
            {millisToDuration((startPosition != null && endPosition != null) ? Math.max(0, endPosition - startPosition) : 0)}
          </Typography>
        </Box>

        <Chip
          size="small"
          label={selectedSubtitle >= 0 ? 'subtitled' : 'no subtitles'}
          variant="outlined"
          sx={{borderColor: 'rgba(255,255,255,0.18)', color: 'text.secondary'}}
        />

        {/* State chip */}
        {status && (
          <Chip
            size="small"
            label={jobStatusLabel(status)}
            sx={{
              fontWeight: 600,
              backgroundColor:
                status === JOB_STATES.SUCCEEDED ? 'rgba(76,175,80,0.18)' :
                status === JOB_STATES.FAILED ? 'rgba(255,90,90,0.18)' :
                status === JOB_STATES.EXPIRED ? 'rgba(255,180,90,0.18)' :
                'rgba(255,115,0,0.18)',
              color:
                status === JOB_STATES.SUCCEEDED ? '#7fd391' :
                status === JOB_STATES.FAILED ? '#ffb4a8' :
                status === JOB_STATES.EXPIRED ? '#ffd9a0' :
                '#ffd9b0',
              border: 'none',
            }}
          />
        )}

      </Box>

      {/* Live job-state announcement for screen readers */}
      {status && (() => {
        const parts = [`Render job status: ${jobStatusLabel(status)}.`]
        if (status === JOB_STATES.SUCCEEDED && downloadUrl) parts.push('Download ready.')
        if (savedToLibrary) parts.push('Saved to your clip library.')
        if (status === JOB_STATES.FAILED && error) parts.push(jobErrorMessage(error))
        if (status === JOB_STATES.EXPIRED) parts.push('The transient download has expired.')
        return (
          <Box aria-live="polite" sx={{position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0,0,0,0)'}}>
            {parts.join(' ')}
          </Box>
        )
      })()}

      {/* Render failure message */}
      {isRenderFailure && error && (
        <Typography variant="caption" sx={{color: '#ffb4a8', mt: 0.75, display: 'block'}} role="alert">
          {jobErrorMessage(error)}
          {error.retryable ? ' You can try again.' : ' Adjust your selection and start a new render.'}
        </Typography>
      )}

      {/* Transport failure message */}
      {isTransportFailure && error && (
        <Typography variant="caption" sx={{color: '#ffd9a0', mt: 0.75, display: 'block'}} role="alert">
          {jobErrorMessage(error)}
        </Typography>
      )}

      {/* Polling/transient error */}
      {busy && error && (
        <Typography variant="caption" sx={{color: '#ffd9a0', mt: 0.75, display: 'block'}} role="status">
          {jobErrorMessage(error)}
          {retryAfter ? ` Retrying in ${retryAfter}s…` : ' Retrying…'}
        </Typography>
      )}

      {/* Expiry info */}
      {status === JOB_STATES.SUCCEEDED && expiresAt && (
        <Typography variant="caption" sx={{color: 'text.disabled', mt: 0.75, display: 'block'}} role="status">
          Download available — {formatExpiry(expiresAt) || 'expires in one hour'}.
        </Typography>
      )}

      {/* Expired message */}
      {status === JOB_STATES.EXPIRED && (
        <Typography variant="caption" sx={{color: '#ffd9a0', mt: 0.75, display: 'block'}} role="status">
          The rendered clip expired. Render again to download.
        </Typography>
      )}

      {/* Hint when idle */}
      {!status && (
        <Typography variant="caption" sx={{display: {md: 'block'}, color: 'text.disabled', mt: 0.75}}>
          Renders to MP4 — large clips may take a few minutes.
        </Typography>
      )}

      {/* "Render new clip" flow when controls changed after a terminal job */}
      {showNewRender && (
        <Box sx={{mt: 1.5, pt: 1.5, borderTop: '1px solid rgba(255,255,255,0.06)'}}>
          <Typography variant="caption" sx={{color: 'text.disabled', display: 'block', mb: 1}}>
            Your selection changed since the last render.
          </Typography>
          <Button variant="outlined" size="small" color="primary" onClick={onCreateNew} sx={{px: 2, py: 0.5}}>
            Render new clip
          </Button>
        </Box>
      )}

      {/* Saved-clip discoverability. A successful render is promoted to a
          durable clip; surface it here without displacing the transient
          download above. The transient download expires after one hour, the
          saved clip does not. */}
      {savedToLibrary && onOpenClip && (
        <Box sx={{mt: 1.5, pt: 1.5, borderTop: '1px solid rgba(255,255,255,0.06)'}}>
          <Typography variant="caption" sx={{color: '#7fd391', display: 'block', mb: 0.75}}>
            Saved to your clip library.
          </Typography>
          <Button
            variant="outlined"
            size="small"
            color="primary"
            onClick={() => onOpenClip(clipId)}
            sx={{px: 2, py: 0.5, borderColor: 'rgba(255,115,0,0.4)'}}
          >
            Open in library
          </Button>
        </Box>
      )}

      {/* The sole sticky mobile affordance sits after its supporting status,
          errors, and saved-clip information in both DOM and tab order. */}
      <Box className="cs-render-primary">
        {busy ? (
          <Button variant="contained" color="primary" disabled sx={{px: 3, py: 1, gap: 1, width: {xs: '100%', sm: 'auto'}}}>
            <CircularProgress size={16} thickness={3} sx={{color: 'currentColor'}}/>
            {status === JOB_STATES.QUEUED ? 'Queued…' : 'Rendering…'}
          </Button>
        ) : status === JOB_STATES.SUCCEEDED && downloadUrl ? (
          <Button variant="contained" color="primary" href={downloadUrl} download onClick={onDownload} sx={{px: 3, py: 1, width: {xs: '100%', sm: 'auto'}}}>Download clip</Button>
        ) : isRenderFailure ? (
          <Button variant="outlined" color="primary" onClick={error.retryable ? onRetry : onCreateNew} sx={{px: 3, py: 1, width: {xs: '100%', sm: 'auto'}}}>{error.retryable ? 'Try again' : 'Start new render'}</Button>
        ) : isTransportFailure ? (
          <Button variant="outlined" color="primary" onClick={onRetryPoll} sx={{px: 3, py: 1, width: {xs: '100%', sm: 'auto'}}}>Retry now</Button>
        ) : status === JOB_STATES.EXPIRED ? (
          <Button variant="outlined" color="primary" onClick={onRetry} sx={{px: 3, py: 1, width: {xs: '100%', sm: 'auto'}}}>Render again</Button>
        ) : (
          <Button variant="contained" color="primary" onClick={onCreateJob} disabled={!ready} sx={{px: 3, py: 1, width: {xs: '100%', sm: 'auto'}}}>Render clip</Button>
        )}
      </Box>
    </Box>
  )
}
