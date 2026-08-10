import {Box, Button, Card, CardContent, Grid} from "@mui/material";
import {useMemo} from "react";
import StateMessage from "./StateMessage";
import SessionCard from "./SessionCard";

const visuallyHidden = {
  position: 'absolute',
  width: 1,
  height: 1,
  padding: 0,
  margin: -1,
  overflow: 'hidden',
  clip: 'rect(0, 0, 0, 0)',
  whiteSpace: 'nowrap',
  border: 0,
};

// Stable owned-first ordering: owned sessions surface first so the user's own
// sessions lead the scan, while the original relative order is preserved within
// each group (stable partition, no secondary sort key). Exported so the
// unified source picker can share the exact same ordering.
export function orderSessionsOwnedFirst(sessions) {
  if (!sessions) return sessions
  const ordered = sessions.reduce((acc, session) => {
    (session.ownedByCurrentUser === true ? acc.owned : acc.rest).push(session)
    return acc
  }, {owned: [], rest: []})
  return [...ordered.owned, ...ordered.rest]
}

// Loading skeleton grid — exported so the unified picker reuses the exact
// same placeholder cards during the initial session load.
export function SessionLoadingGrid() {
  return (
    <>
      <Box component="span" role="status" aria-live="polite" sx={visuallyHidden}>
        Loading sessions…
      </Box>
      <Grid
        container
        spacing={3}
        sx={{mx: 'auto', px: {xs: 2, sm: 3}}}
        aria-busy="true"
        aria-label="Loading active Plex sessions"
      >
        {[0, 1, 2, 3, 4, 5].map(i => (
          <Grid item xs={12} sm={6} md={4} key={i}>
            <Card sx={{height: '100%', display: 'flex', flexDirection: 'column', overflow: 'hidden'}}>
              <Box sx={{
                width: '100%', aspectRatio: '16 / 9', flexShrink: 0,
                background: 'linear-gradient(110deg,#222633,#1b1e26)',
                borderBottom: '1px solid rgba(255,255,255,0.06)',
              }}/>
              <CardContent sx={{display: 'flex', flexDirection: 'column', gap: 1.25}}>
                <Box sx={{height: 24, width: '76%', borderRadius: 1, background: '#2a2f3a'}}/>
                <Box sx={{height: 16, width: '42%', borderRadius: 1, background: '#232733', mb: 0.5}}/>
                <Box sx={{height: 6, width: '100%', borderRadius: 999, background: '#232733', mb: 0.75}}/>
                <Box sx={{height: 8, width: '38%', borderRadius: 999, background: '#2a2f3a'}}/>
                <Box sx={{height: 12, width: '64%', borderRadius: 1, background: '#232733', mt: 0.5}}/>
              </CardContent>
            </Card>
          </Grid>
        ))}
      </Grid>
    </>
  )
}

// Session error state — exported for reuse in the unified picker.
export function SessionErrorState({onRetry}) {
  return (
    <StateMessage
      variant="error"
      title="Couldn’t load your Plex sessions."
      hint="Check that the CutScene server is reachable and Plex is linked."
      live
      action={onRetry && (
        <Button variant="outlined" color="primary" size="small" onClick={onRetry}
          sx={{mt: 1, borderColor: 'rgba(255,255,255,0.2)'}}>
          Try again
        </Button>
      )}
    />
  )
}

// No-sessions empty state — exported for reuse in the unified picker.
export function SessionEmptyState() {
  return (
    <StateMessage
      variant="empty"
      title="No active Plex sessions."
      hint="Start playing something in Plex and it will appear here."
      live
    />
  )
}

export default function SessionPicker({sessions, loading, error, selectedKey, onSelect, onRetry}) {
  const sortedSessions = useMemo(() => orderSessionsOwnedFirst(sessions), [sessions])

  if (loading) return <SessionLoadingGrid/>
  if (error) return <SessionErrorState onRetry={onRetry}/>
  if (!sessions || sessions.length === 0) return <SessionEmptyState/>

  return (
    <Grid container spacing={3} sx={{mx: 'auto', px: {xs: 2, sm: 3}}}>
      {sortedSessions.map(session => (
        <SessionCard
          key={session.ratingKey}
          session={session}
          active={session.ratingKey === selectedKey}
          onSelect={onSelect}
        />
      ))}
    </Grid>
  )
}
