import {Box, Button, Typography} from "@mui/material";
import ReactPlayer from "react-player";

// Player pane. Receives the preview URL and surfaces load errors via onError.
// Responsive via CSS aspect-ratio container — no duplicate DOM per breakpoint.
// When previewStale is true, shows a "Preview selection" button overlay so the
// user controls when the player reloads, preventing jank during trim editing.
export default function PlayerPane({playerUrl, onError, onReady, previewStale, onApplyPreview}) {
  return (
    <Box className="cs-rise">
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