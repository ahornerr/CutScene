import {Button, CircularProgress, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle, Typography} from "@mui/material";

// Delete confirmation flow. The API permits delete only for the clip owner or
// a server administrator (`canDelete` is explicit per clip). The dialog
// confirms intent rather than gating on permission.
//
// Error recovery:
//   - `network` (5xx): keep the dialog open with a retryable alert so the
//     user does not lose context.
//   - `auth` (401/403): close the retry path and show a sign-in/reload
//     action. Retrying would be futile — the session is gone.
//   - `not_found` (404): treated as success by the caller before reaching
//     the dialog.
export function ClipDeleteDialog({open, clip, deleting, error, onConfirm, onClose}) {
  const title = clip?.title || 'this clip'
  const isAuthError = error?.code === 'auth'

  return (
    <Dialog
      open={open}
      onClose={deleting ? undefined : onClose}
      aria-labelledby="clip-delete-title"
      aria-describedby="clip-delete-desc"
      maxWidth="xs"
      fullWidth
    >
      <DialogTitle id="clip-delete-title" sx={{fontWeight: 700, overflowWrap: 'anywhere', lineHeight: 1.2}}>
        Delete {title}?
      </DialogTitle>
      <DialogContent>
        <DialogContentText id="clip-delete-desc" sx={{color: 'text.secondary'}}>
          This permanently removes the clip and its video file. This cannot be undone.
        </DialogContentText>
        {error && (
          <Typography role="alert" sx={{color: '#ffb4a8', mt: 1.5, fontSize: '0.85rem'}}>
            {isAuthError
              ? 'Your session has expired. Reload the page and sign in again to delete clips.'
              : (error.message || 'Could not delete this clip right now.')}
            {!isAuthError && error.code === 'network' ? ' Try again.' : ''}
          </Typography>
        )}
      </DialogContent>
      <DialogActions sx={{px: {xs: 2, sm: 3}, pb: {xs: 2, sm: 2.5}, gap: 1, flexDirection: {xs: 'column', sm: 'row'}, alignItems: 'stretch'}}>
        {isAuthError ? (
          <>
            <Button
              onClick={onClose}
              disabled={deleting}
              variant="outlined"
              color="primary"
              sx={{borderColor: 'rgba(255,255,255,0.2)', width: {xs: '100%', sm: 'auto'}}}
            >
              Cancel
            </Button>
            <Button
              variant="contained"
              color="primary"
              href="/authUrl"
              sx={{px: 3, py: 1, width: {xs: '100%', sm: 'auto'}}}
            >
              Reload and sign in
            </Button>
          </>
        ) : (
          <>
            <Button
              onClick={onClose}
              disabled={deleting}
              variant="outlined"
              color="primary"
              sx={{borderColor: 'rgba(255,255,255,0.2)', width: {xs: '100%', sm: 'auto'}}}
            >
              Cancel
            </Button>
            <Button
              onClick={onConfirm}
              disabled={deleting}
              variant="contained"
              color="error"
              startIcon={deleting ? <CircularProgress size={16} thickness={3} sx={{color: 'currentColor'}}/> : null}
              sx={{
                backgroundColor: '#c0392b', color: '#fff', width: {xs: '100%', sm: 'auto'},
                '&:hover': {backgroundColor: '#a93226'},
              }}
            >
              {deleting ? 'Deleting…' : 'Delete clip'}
            </Button>
          </>
        )}
      </DialogActions>
    </Dialog>
  )
}
