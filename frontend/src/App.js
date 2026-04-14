import './App.css';
import {useCallback, useEffect, useMemo, useRef, useState} from "react";
import ReactPlayer from "react-player";
import {Box, Button, Card, CardActionArea, CardContent, CardMedia, CircularProgress, FormControl, InputLabel, MenuItem, Select, Slider, TextField, Typography} from "@mui/material";
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
  const [subtitleStreams, setSubtitleStreams] = useState([])
  const [selectedSubtitle, setSelectedSubtitle] = useState(-1)
  const [subtitleEntries, setSubtitleEntries] = useState([])
  const [subtitleSearch, setSubtitleSearch] = useState('')
  const [subtitlesLoading, setSubtitlesLoading] = useState(false)
  const [subtitleAnchor, setSubtitleAnchor] = useState(-1)
  const [subtitleSelectionEnd, setSubtitleSelectionEnd] = useState(-1)
  const subtitleListRef = useRef(null)

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
    const subtitleParam = selectedSubtitle >= 0 ? `&subtitle=${selectedSubtitle}` : ''
    setPlayerUrl(`/preview/${selectedSession.ratingKey}/${millisToDuration(startPosition)}/${millisToDuration(endPosition)}?mediaId=${selectedSession.Media[0].Part[0].id}${subtitleParam}`)
  }, [selectedSession, selectedSubtitle]);

  useEffect(() => {
    if (selectedSession) {
      setStartPosition(selectedSession.viewOffset)
      setEndPosition(selectedSession.viewOffset + 60000)
      setSubtitleStreams([])
      setSelectedSubtitle(-1)
      fetch(`/streams/${selectedSession.ratingKey}`)
        .then(r => r.json())
        .then(streams => {
          setSubtitleStreams(streams || [])
          if (streams && streams.length > 0) {
            setSelectedSubtitle(streams[0].index)
          }
        })
        .catch(err => console.log('Could not fetch subtitle streams:', err))
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedSession])

  useEffect(() => {
    if (!selectedSession || selectedSubtitle < 0) {
      setSubtitleEntries([])
      setSubtitleSearch('')
      return
    }

    setSubtitlesLoading(true)
    setSubtitleEntries([])

    const mediaId = selectedSession.Media[0].Part[0].id
    fetch(`/subtitles/${selectedSession.ratingKey}?subtitle=${selectedSubtitle}&mediaId=${mediaId}`)
      .then(r => r.json())
      .then(entries => setSubtitleEntries(entries || []))
      .catch(err => console.error('Could not fetch subtitle entries:', err))
      .finally(() => setSubtitlesLoading(false))
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedSession, selectedSubtitle])

  const debounceSetPosition = useMemo(
    () => debounce(setPlayerPosition, 500),
    [setPlayerPosition],
  );

  const filteredSubtitleEntries = useMemo(() => {
    if (!subtitleSearch.trim()) return subtitleEntries
    const lower = subtitleSearch.toLowerCase()
    return subtitleEntries.filter(e => e.text.toLowerCase().includes(lower))
  }, [subtitleEntries, subtitleSearch])

  const subtitleSelectionRange = useMemo(() => {
    if (subtitleAnchor < 0) return { from: -1, to: -1 }
    const end = subtitleSelectionEnd >= 0 ? subtitleSelectionEnd : subtitleAnchor
    return { from: Math.min(subtitleAnchor, end), to: Math.max(subtitleAnchor, end) }
  }, [subtitleAnchor, subtitleSelectionEnd])

  const applySubtitleSelection = useCallback((from, to) => {
    const firstEntry = subtitleEntries[from]
    const lastEntry = subtitleEntries[to]
    if (!firstEntry || !lastEntry) return
    setEndPosition(lastEntry.end + 500)
    setStartPosition(Math.max(0, firstEntry.start - 500))
  }, [subtitleEntries])

  // Scroll full list to anchor when it changes
  useEffect(() => {
    if (subtitleAnchor < 0 || !subtitleListRef.current) return
    const el = subtitleListRef.current.querySelector(`[data-idx="${subtitleAnchor}"]`)
    if (el) el.scrollIntoView({ block: 'center' })
  }, [subtitleAnchor])

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

            {subtitleStreams.length > 0 && (
              <Box sx={{width: '50%', mb: 3}} display='flex' justifyContent='center' alignItems='center'>
                <Box sx={{width: 200, mr: 3}}>
                  <FormControl fullWidth>
                    <InputLabel id="subtitle-select">Subtitles</InputLabel>
                    <Select
                      labelId="subtitle-select"
                      value={selectedSubtitle}
                      onChange={e => setSelectedSubtitle(e.target.value)}
                    >
                      <MenuItem value={-1}>None</MenuItem>
                      {subtitleStreams.map((stream, idx) => (
                        <MenuItem key={idx} value={stream.index}>
                          {stream.language
                            ? `${stream.language} (${stream.displayTitle || 'Track ' + (stream.index + 1)})`
                            : stream.displayTitle || 'Track ' + (stream.index + 1)}
                        </MenuItem>
                      ))}
                    </Select>
                  </FormControl>
                </Box>
                <Box sx={{flex: 1}}>
                  <Typography variant="body2" color="text.secondary">
                    {subtitleStreams.find(s => s.index === selectedSubtitle)?.codec}
                  </Typography>
                </Box>
              </Box>
            )}

            {selectedSubtitle >= 0 && (
              <Box sx={{width: '50%', mb: 3}}>
                {subtitlesLoading ? (
                  <Box sx={{height: 340, display: 'flex', justifyContent: 'center', alignItems: 'center'}}>
                    <CircularProgress />
                    <Typography variant="body2" color="text.secondary" sx={{ml: 2}}>Loading subtitles...</Typography>
                  </Box>
                ) : subtitleEntries.length > 0 && (
                  <>
                    {/* Search box */}
                    <TextField
                      fullWidth
                      variant="outlined"
                      size="small"
                      label="Search subtitles"
                      value={subtitleSearch}
                      onChange={e => setSubtitleSearch(e.target.value)}
                    />

                    {/* Conditional: search results OR full list */}
                    {subtitleSearch.trim() ? (
                      /* Search results - clicking jumps to entry in full list and clears search */
                      <Box sx={{height: 300, overflowY: 'auto', border: '1px solid #444', borderRadius: 1, mt: 1}}>
                        {filteredSubtitleEntries.map((entry, idx) => (
                          <Box
                            key={idx}
                            onClick={() => {
                              const fullIdx = subtitleEntries.indexOf(entry)
                              setSubtitleAnchor(fullIdx)
                              setSubtitleSelectionEnd(fullIdx)
                              applySubtitleSelection(fullIdx, fullIdx)
                              setSubtitleSearch('')
                            }}
                            sx={{
                              display: 'flex',
                              alignItems: 'center',
                              px: 1.5,
                              py: 0.75,
                              cursor: 'pointer',
                              borderBottom: '1px solid #333',
                              '&:hover': { backgroundColor: '#383c44' },
                              userSelect: 'none',
                            }}
                          >
                            <Typography variant="caption" color="text.secondary" sx={{minWidth: 70, fontFamily: 'monospace', flexShrink: 0}}>
                              {millisToDuration(entry.start)}
                            </Typography>
                            <Typography variant="body2" sx={{ml: 1.5}}>
                              {entry.text}
                            </Typography>
                          </Box>
                        ))}
                        {filteredSubtitleEntries.length === 0 && (
                          <Box sx={{p: 2, textAlign: 'center', color: 'text.secondary'}}>
                            No matching subtitles found
                          </Box>
                        )}
                      </Box>
                    ) : (
                      /* Full subtitle list - supports range selection with shift-click */
                      <Box sx={{height: 300, overflowY: 'auto', border: '1px solid #444', borderRadius: 1, mt: 1}} ref={subtitleListRef}>
                        {subtitleEntries.map((entry, idx) => {
                          const isSelected = idx >= subtitleSelectionRange.from && idx <= subtitleSelectionRange.to
                          return (
                            <Box
                              key={idx}
                              data-idx={idx}
                              onClick={e => {
                                if (e.shiftKey && subtitleAnchor >= 0) {
                                  const newEnd = idx
                                  setSubtitleSelectionEnd(newEnd)
                                  applySubtitleSelection(
                                    Math.min(subtitleAnchor, newEnd),
                                    Math.max(subtitleAnchor, newEnd)
                                  )
                                } else {
                                  setSubtitleAnchor(idx)
                                  setSubtitleSelectionEnd(idx)
                                  applySubtitleSelection(idx, idx)
                                }
                              }}
                              sx={{
                                display: 'flex',
                                alignItems: 'center',
                                px: 1.5,
                                py: 0.75,
                                cursor: 'pointer',
                                borderBottom: '1px solid #333',
                                backgroundColor: isSelected ? '#1a3a5c' : 'transparent',
                                '&:hover': { backgroundColor: isSelected ? '#1e4676' : '#383c44' },
                                userSelect: 'none',
                              }}
                            >
                              <Typography variant="caption" color="text.secondary" sx={{minWidth: 70, fontFamily: 'monospace', flexShrink: 0}}>
                                {millisToDuration(entry.start)}
                              </Typography>
                              <Typography variant="body2" sx={{ml: 1.5}}>
                                {entry.text}
                              </Typography>
                            </Box>
                          )
                        })}
                      </Box>
                    )}
                  </>
                )}
              </Box>
            )}

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
                href={`/clip/${selectedSession.ratingKey}/${millisToDuration(startPosition)}/${millisToDuration(endPosition)}?mediaId=${selectedSession.Media[0].Part[0].id}${selectedSubtitle >= 0 ? `&subtitle=${selectedSubtitle}` : ''}`}
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
