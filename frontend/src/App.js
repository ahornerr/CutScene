import './App.css';
import {useCallback, useEffect, useMemo, useState} from "react";
import ReactPlayer from "react-player";
import {Box, Button, Card, CardActionArea, CardContent, CardMedia, Slider, TextField, Typography} from "@mui/material";
import {debounce} from '@mui/material/utils'
import {TimePicker} from "@mui/x-date-pickers";
import moment from "moment";

function millisToDuration(millis) {
  const hours = Math.floor(millis / 1000 / 60 / 60)
  const minutes = Math.floor(millis / 1000 / 60) % 60
  const seconds = Math.floor(millis / 1000) % 60
  millis = millis % 1000
  if (millis === 0) {
    return String(hours).padStart(2, '0') + ':' + String(minutes).padStart(2, '0') + ':' + String(seconds).padStart(2, '0');
  } else {
    return String(hours).padStart(2, '0') + ':' + String(minutes).padStart(2, '0') + ':' + String(seconds).padStart(2, '0') + '.' + String(millis).padEnd(3, '0');
  }
}

function getVideoName(session) {
  const type = session.type
  const title = session.title

  if (type === "episode") {
    return <>
      <div>{session.grandparentTitle}</div>
      S{String(session.parentIndex).padStart(2, '0')}E{String(session.index).padStart(2, '0')} {title}
    </>
  } else {
    return `${title} (${session.year})`
  }
}

function convertPositionToDate(position) {
  return moment().startOf('day').add(position, 'ms')
}

function convertDateToPosition(date) {
  return date.diff(date.clone().startOf('day'))
}

function App() {
  const [sessions, setSessions] = useState(null)
  const [selectedSession, setSelectedSession] = useState(null)
  const [startPosition, setStartPosition] = useState(null)
  const [endPosition, setEndPosition] = useState(null)
  const [playerUrl, setPlayerUrl] = useState(null)
  const [needsAuth, setNeedsAuth] = useState(false)

  useEffect(() => {
    fetch('/sessions', {redirect: "manual"})
      .then(response => {
        // This is hack but whatever https://stackoverflow.com/questions/39735496/redirect-after-a-fetch-post-call
        if (response.type === "opaqueredirect") {
          setNeedsAuth(true)
          return null
        }

        if (!response.ok) {
          console.error(`Error: ${response.status} ${response.statusText}`);
          console.error(`Error details: ${response.errorText || 'No error message provided'}`);
          throw new Error('Error fetching sessions');
        }

        return response.json()
      })
      .then(json => setSessions(json || []))
      .catch(err => console.log(err));
  }, [setNeedsAuth, setSessions])

  const setPlayerPosition = useCallback((startPosition, endPosition) => {
    setPlayerUrl(`/preview/${selectedSession.ratingKey}/${millisToDuration(startPosition)}/${millisToDuration(endPosition)}?mediaId=${selectedSession.Media[0].Part[0].id}`)
  }, [selectedSession]);

  useEffect(() => {
    if (selectedSession) {
      setStartPosition(selectedSession.viewOffset)
      setEndPosition(selectedSession.viewOffset + 60000)
    }
  }, [selectedSession, setStartPosition, setEndPosition])

  const debounceSetPosition = useMemo(
    () => debounce(setPlayerPosition, 500),
    [setPlayerPosition],
  );

  useEffect(() => {
    if (startPosition) {
      if (playerUrl === null) {
        setPlayerPosition(startPosition, endPosition)
      } else {
        debounceSetPosition(startPosition, endPosition)
      }
    }
  }, [startPosition, endPosition, debounceSetPosition, playerUrl, setPlayerPosition])

  const setBoundedStartPosition = (newValue) => {
    if (newValue < (endPosition - 500)) {
      setStartPosition(newValue)
    }
  }

  const setBoundedEndPosition = (newValue) => {
    if (newValue > (startPosition + 500)) {
      setEndPosition(newValue)
    }
  }

  return (
    <div className="App">
      <header className="App-header">
        <h1>CutScene</h1>
        {needsAuth ? (
          <Button variant="contained" onClick={() => window.location.replace('/authUrl')}>
            Log in with Plex
          </Button>
        ) : (sessions ? (
              <Box display='flex' justifyContent='center' alignItems='center'>
                {sessions.length > 0 ?
                  sessions.map(session => (
                    <Card
                      sx={{
                        m: 2,
                        display: 'flex',
                        bgcolor: session.ratingKey === selectedSession?.ratingKey ? '#666666' : null
                      }}
                    >
                      <Box sx={{display: 'flex', flexDirection: 'column'}}>
                        <CardActionArea sx={{height: '100%'}} onClick={() => setSelectedSession(session)}>
                          <CardContent sx={{flex: '1 0 auto', width: 360}}>
                            <Typography gutterBottom variant="h6" component="div">
                              {getVideoName(session)}
                            </Typography>
                            <Typography variant="body2" color="text.secondary">
                              {millisToDuration(session.viewOffset)}
                            </Typography>
                            <Typography variant="body2" color="text.secondary">
                              {session.User.title}
                            </Typography>
                          </CardContent>
                        </CardActionArea>

                      </Box>
                      <CardMedia component="img" sx={{height: 160, width: 160}} image={`/thumb?path=${session.thumb}`}/>
                    </Card>
                  ))
                  :
                  "No active sessions found"
                }
              </Box>
            )
            :
            'Loading...'
        )
        }
        {!!selectedSession && (
          <>
            <ReactPlayer
              url={playerUrl}
              controls={true}
              playing={true}
              // muted={true}
              onError={(err, data) => console.error(err, data)}
              height="720px"
              width="50%"
              config={{
                file: {
                  attributes: {
                    preload: "auto"
                  }
                }
              }}
            />

            <Box sx={{width: '75%'}} mt={6} display='flex' justifyContent='center' alignItems='center'>
              <Box sx={{width: 80}}>
                <Typography variant="h6">Start</Typography>
              </Box>

              <Box sx={{width: 170, mr: 3}}>
                <TimePicker
                  views={['hours', 'minutes', 'seconds']}
                  ampm={false}
                  value={convertPositionToDate(startPosition)}
                  onChange={newValue => setBoundedStartPosition(convertDateToPosition(newValue))}
                  slotProps={{textField: {variant: "outlined", size: "small"}}}
                />
                <TextField
                  sx={{mt: 1}}
                  variant="outlined"
                  type="number"
                  label="+ms"
                  size="small"
                  value={startPosition % 1000}
                  onChange={event => {
                    const value = parseInt(event.target.value)
                    if (value > 999 || value < 0) {
                      return
                    }
                    const startPositionWithoutMillis = Math.floor(startPosition / 1000) * 1000
                    setBoundedStartPosition(startPositionWithoutMillis + value)
                  }}
                />
              </Box>

              <Slider
                min={0}
                step={1}
                max={selectedSession.duration}
                value={startPosition}
                onChange={(_, newValue) => setBoundedStartPosition(newValue)}
                valueLabelFormat={millisToDuration}
                valueLabelDisplay="auto"
                marks={[
                  {
                    value: endPosition,
                    label: "End",
                  },
                ]}
              />
            </Box>

            <Box sx={{width: '75%'}} mt={6} display='flex' justifyContent='center' alignItems='center'>
              <Box sx={{width: 80}}>
                <Typography variant="h6">End</Typography>
              </Box>

              <Box sx={{width: 170, mr: 3}}>
                <TimePicker
                  views={['hours', 'minutes', 'seconds']}
                  ampm={false}
                  value={convertPositionToDate(endPosition)}
                  onChange={newValue => setBoundedEndPosition(convertDateToPosition(newValue))}
                  slotProps={{textField: {variant: "outlined", size: "small"}}}
                />
                <TextField
                  sx={{mt: 1}}
                  variant="outlined"
                  type="number"
                  label="+ms"
                  size="small"
                  value={endPosition % 1000}
                  onChange={event => {
                    const value = parseInt(event.target.value)
                    if (value > 999 || value < 0) {
                      return
                    }
                    const endPositionWithoutMillis = Math.floor(endPosition / 1000) * 1000
                    setBoundedEndPosition(endPositionWithoutMillis + value)
                  }}
                />
              </Box>

              <Slider
                min={0}
                step={1}
                max={selectedSession.duration}
                value={endPosition}
                onChange={(_, newValue) => setBoundedEndPosition(newValue)}
                valueLabelFormat={millisToDuration}
                valueLabelDisplay="auto"
                marks={[
                  {
                    value: startPosition,
                    label: "Start",
                  },
                ]}
              />
            </Box>

            <Box my={6}>
              <Button
                variant="contained"
                href={`/clip/${selectedSession.ratingKey}/${millisToDuration(startPosition)}/${millisToDuration(endPosition)}?mediaId=${selectedSession.Media[0].Part[0].id}`}
                target="_blank"
                download
              >
                Download
              </Button>
            </Box>

          </>
        )}
      </header>
    </div>
  );
}

export default App;
