import {Box, Button, Typography} from "@mui/material";

// Session context bar — makes the active session unmistakable in the workspace.
// Shows the title/context and provides a graceful Change-session affordance.
// The Change-session button is disabled when a render job is active.
function getVideoName(session) {
  if (session.type === "episode") {
    return {
      top: session.grandparentTitle,
      bottom: `S${String(session.parentIndex).padStart(2, '0')}E${String(session.index).padStart(2, '0')} ${session.title}`,
    }
  }
  return {top: session.title, bottom: session.year ? `(${session.year})` : ''}
}

export default function SessionContext({session, onChangeSession, changeDisabled, changeDisabledReason}) {
  const name = getVideoName(session)
  return (
    <Box className="cs-session-context cs-rise">
      <Box sx={{flex: 1, minWidth: 0}}>
        <Typography variant="overline" className="cs-section-label" sx={{color: '#ff7300'}}>
          Now clipping
        </Typography>
        <Typography noWrap sx={{fontWeight: 600, fontSize: '1.15rem', mt: 0.25}}>
          {name.top}
        </Typography>
        {name.bottom && (
          <Typography noWrap variant="body2" sx={{color: 'text.secondary', mt: 0.15}}>
            {name.bottom}
          </Typography>
        )}
      </Box>
      <Button
        variant="outlined"
        size="small"
        onClick={onChangeSession}
        disabled={changeDisabled}
        title={changeDisabled ? changeDisabledReason : ''}
        sx={{borderColor: 'rgba(255,255,255,0.2)', flexShrink: 0}}
      >
        Change session
      </Button>
    </Box>
  )
}