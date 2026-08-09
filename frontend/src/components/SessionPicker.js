import {Box, Card, CardActionArea, CardContent, CardMedia, Grid, Typography} from "@mui/material";
import StateMessage from "./StateMessage";

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

function millisToDuration(millis) {
  const hours = Math.floor(millis / 1000 / 60 / 60)
  const minutes = Math.floor(millis / 1000 / 60) % 60
  const seconds = Math.floor(millis / 1000) % 60
  return String(hours).padStart(2, '0') + ':' + String(minutes).padStart(2, '0') + ':' + String(seconds).padStart(2, '0')
}

function getVideoName(session) {
  if (session.type === "episode") {
    return {
      top: session.grandparentTitle,
      bottom: `S${String(session.parentIndex).padStart(2, '0')}E${String(session.index).padStart(2, '0')} ${session.title}`,
    }
  }
  return {top: `${session.title}`, bottom: session.year ? `(${session.year})` : ''}
}

export default function SessionPicker({sessions, loading, error, selectedKey, onSelect, onRetry}) {
  if (loading) {
    return (
      <>
        <Box component="span" role="status" aria-live="polite" sx={visuallyHidden}>
          Loading sessions…
        </Box>
        <Grid
          container
          spacing={2.5}
          sx={{maxWidth: 1100, mx: 'auto', px: {xs: 2, sm: 3}}}
          aria-busy="true"
          aria-label="Loading active Plex sessions"
        >
          {[0, 1, 2].map(i => (
            <Grid item xs={12} sm={6} md={4} key={i}>
              <Card sx={{height: '100%'}}>
                <Box sx={{height: 168, background: 'linear-gradient(110deg,#222633,#1b1e26)', borderBottom: '1px solid rgba(255,255,255,0.06)'}}/>
                <CardContent>
                  <Box sx={{height: 22, width: '80%', borderRadius: 1, background: '#2a2f3a', mb: 1.5}}/>
                  <Box sx={{height: 14, width: '45%', borderRadius: 1, background: '#232733'}}/>
                </CardContent>
              </Card>
            </Grid>
          ))}
        </Grid>
      </>
    )
  }

  if (error) {
    return (
      <StateMessage
        variant="error"
        title="Couldn’t load your Plex sessions."
        hint="Check that the CutScene server is reachable and Plex is linked."
        live
      />
    )
  }

  if (!sessions || sessions.length === 0) {
    return (
      <StateMessage
        variant="empty"
        title="No active Plex sessions."
        hint="Start playing something in Plex and it will appear here."
        live
      />
    )
  }

  return (
    <Grid container spacing={2.5} sx={{maxWidth: 1100, mx: 'auto', px: {xs: 2, sm: 3}}}>
      {sessions.map(session => {
        const name = getVideoName(session)
        const active = session.ratingKey === selectedKey
        return (
          <Grid item xs={12} sm={6} md={4} key={session.ratingKey}>
            <Card
              sx={{
                height: '100%',
                transition: 'transform 180ms ease, border-color 180ms ease, box-shadow 180ms ease',
                borderColor: active ? 'rgba(255,115,0,0.55)' : undefined,
                boxShadow: active ? '0 14px 40px -22px rgba(255,115,0,0.7)' : undefined,
                '&:hover': {transform: 'translateY(-2px)', borderColor: 'rgba(255,115,0,0.4)'},
              }}
            >
              <CardActionArea
                onClick={() => onSelect(session)}
                sx={{display: 'flex', alignItems: 'stretch', height: '100%'}}
                focusRipple
              >
                <CardMedia
                  component="img"
                  sx={{width: 132, flexShrink: 0, objectFit: 'cover'}}
                  image={`/thumb?path=${session.thumb}`}
                  alt=""
                />
                <CardContent sx={{flex: 1, alignSelf: 'center', minWidth: 0}}>
                  <Typography noWrap sx={{fontWeight: 600, fontSize: '1.02rem'}}>
                    {name.top}
                  </Typography>
                  <Typography noWrap variant="body2" sx={{color: 'text.secondary', mt: 0.2}}>
                    {name.bottom}
                  </Typography>
                  <Box sx={{display: 'flex', gap: 1.5, mt: 1.5, flexWrap: 'wrap'}}>
                    <Typography variant="caption" sx={{fontFamily: 'var(--cs-mono-font)', color: '#ff7300'}}>
                      {millisToDuration(session.viewOffset)}
                    </Typography>
                    <Typography variant="caption" sx={{color: 'text.secondary'}}>
                      {session.User?.title}
                    </Typography>
                  </Box>
                </CardContent>
              </CardActionArea>
            </Card>
          </Grid>
        )
      })}
    </Grid>
  )
}
