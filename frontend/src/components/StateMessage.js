import {Box, CircularProgress, Typography} from "@mui/material";

// Shared loading / empty / error affordance with a live-region option. An
// optional `action` node (e.g. a retry button) renders under the hint.
export default function StateMessage({variant = 'empty', title, hint, live = false, action}) {
  const tone =
    variant === 'error' ? '#ffb4a8' :
    variant === 'loading' ? 'text.secondary' : 'text.secondary';

  return (
    <Box
      role={variant === 'error' ? 'alert' : undefined}
      aria-live={live ? 'polite' : undefined}
      aria-busy={variant === 'loading' || undefined}
      sx={{
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        gap: 1.5,
        py: 5,
        px: 3,
        textAlign: 'center',
      }}
    >
      {variant === 'loading' && <CircularProgress size={30} thickness={3} />}
      <Typography variant="body2" sx={{color: tone, maxWidth: 360}}>
        {title}
      </Typography>
      {hint && (
        <Typography variant="caption" sx={{color: 'text.disabled', maxWidth: 360}}>
          {hint}
        </Typography>
      )}
      {action}
    </Box>
  );
}