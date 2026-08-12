import {Box, Button, ButtonGroup, FormHelperText, Slider, TextField, Typography} from "@mui/material";
import {useEffect, useMemo, useRef, useState} from "react";
import {millisToDuration, MIN_GAP_MS} from "../utils";

const ZOOM_PRESETS = [
  {label: '5m', value: 300000}, {label: '1m', value: 60000},
  {label: '30s', value: 30000}, {label: '10s', value: 10000},
]
const STEP_PRESETS = [
  {label: 'Fine', value: 100}, {label: 'Normal', value: 1000}, {label: 'Coarse', value: 5000},
]
const FOCUS_MARGIN_MS = 2000

function stepLabel(stepMs) {
  if (stepMs < 1000) return `${stepMs} milliseconds`
  const seconds = stepMs / 1000
  return `${seconds} second${seconds === 1 ? '' : 's'}`
}

function focusBounds(start, end, duration, requestedWindow) {
  const selection = Math.max(MIN_GAP_MS, end - start)
  const windowSize = Math.min(duration, Math.max(requestedWindow, selection + FOCUS_MARGIN_MS * 2))
  const midpoint = (start + end) / 2
  let from = Math.max(0, midpoint - windowSize / 2)
  let to = Math.min(duration, from + windowSize)
  from = Math.max(0, to - windowSize)
  return [Math.round(from), Math.round(to)]
}

// The overview keeps long media legible; the focused rail gives nearby endpoints
// enough physical space for deliberate edits. Both rails edit local draft state
// and only notify the workspace when the gesture is committed.
export default function TrimScrubber({
  duration, startPosition, endPosition,
  onRangeChange, onStartChange, onEndChange,
  trimFlashKey = 0,
}) {
  const [sliderValue, setSliderValue] = useState([startPosition, endPosition])
  const [zoomMs, setZoomMs] = useState(60000)
  const [focusWindow, setFocusWindow] = useState(() => focusBounds(startPosition || 0, endPosition || 0, duration || 0, 60000))
  const [stepMs, setStepMs] = useState(1000)

  useEffect(() => {
    setSliderValue([startPosition, endPosition])
    setFocusWindow(focusBounds(startPosition, endPosition, duration, zoomMs))
  }, [startPosition, endPosition, duration, zoomMs])

  const selectionLength = sliderValue[1] - sliderValue[0]
  const focusLabel = useMemo(
    () => `${millisToDuration(focusWindow[0])} to ${millisToDuration(focusWindow[1])}`,
    [focusWindow]
  )

  const setDraftRange = (next) => {
    const [s, e] = next
    if (s >= 0 && e <= duration && e - s >= MIN_GAP_MS) {
      setSliderValue(next)
      if (s < focusWindow[0] || e > focusWindow[1]) setFocusWindow(focusBounds(s, e, duration, zoomMs))
    }
  }
  const commitRange = (_, next) => {
    const [s, e] = next
    if (e - s >= MIN_GAP_MS) onRangeChange(s, e)
  }
  const chooseZoom = (nextZoom) => {
    setZoomMs(nextZoom)
    setFocusWindow(focusBounds(sliderValue[0], sliderValue[1], duration, nextZoom))
  }
  const resetFocus = () => {
    setZoomMs(duration)
    setFocusWindow([0, duration])
  }
  const nudge = (isStart, direction) => {
    const [start, end] = sliderValue
    const candidate = isStart ? start + direction * stepMs : end + direction * stepMs
    const next = isStart ? [candidate, end] : [start, candidate]
    if (next[0] < 0 || next[1] > duration || next[1] - next[0] < MIN_GAP_MS) return
    setSliderValue(next)
    setFocusWindow(focusBounds(next[0], next[1], duration, zoomMs))
    // Nudge presses are discrete, intentional edits; commit immediately.
    if (isStart) onStartChange(candidate)
    else onEndChange(candidate)
  }

  if (startPosition == null || endPosition == null) {
    return <Box className="cs-rise-2" sx={{mt: 2.5, height: 96, display: 'grid', placeItems: 'center'}}><Typography variant="body2" sx={{color: 'text.secondary'}}>Preparing trim…</Typography></Box>
  }

  return (
    <Box className="cs-rise-2 cs-trim-editor" sx={{mt: {xs: 2, sm: 2.5}}}>
      <Timeline
        label="Full media timeline"
        detail={`Selection ${millisToDuration(selectionLength)}`}
        min={0} max={duration} value={sliderValue} step={1000}
        onChange={(_, next) => setDraftRange(next)} onCommit={commitRange} shiftStep={5000}
      />

      <Box className="cs-trim-focus-controls" sx={{display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap', mt: 1.25}}>
        <Typography variant="caption" sx={{color: 'text.secondary', fontWeight: 700}}>Focus range</Typography>
        <ButtonGroup size="small" aria-label="Focused timeline zoom">
          {ZOOM_PRESETS.map(preset => <Button key={preset.value} onClick={() => chooseZoom(preset.value)} aria-pressed={zoomMs === preset.value} variant={zoomMs === preset.value ? 'contained' : 'outlined'}>{preset.label}</Button>)}
        </ButtonGroup>
        <Button size="small" onClick={resetFocus} disabled={focusWindow[0] === 0 && focusWindow[1] === duration}>Full timeline</Button>
      </Box>

      <Box className="cs-trim-focus" sx={{position: 'relative', mt: 1.25, px: {xs: 1.25, sm: 1}, py: 1, borderRadius: 2, backgroundColor: 'rgba(255,115,0,0.055)', border: '1px solid rgba(255,115,0,0.16)'}}>
        {trimFlashKey > 0 && <Box key={trimFlashKey} className="cs-trim-flash" sx={{position: 'absolute', inset: -4, borderRadius: 2, pointerEvents: 'none'}}/>}
        <Timeline
          label="Focused trim timeline"
          detail={focusLabel}
          min={focusWindow[0]} max={focusWindow[1]} value={sliderValue} step={stepMs}
          onChange={(_, next) => setDraftRange(next)} onCommit={commitRange}
          shiftStep={stepMs * 5}
        />
      </Box>

      <Box sx={{display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap', mt: 1.25}}>
        <Typography variant="caption" sx={{color: 'text.secondary', fontWeight: 700}}>Step</Typography>
        <ButtonGroup size="small" aria-label="Trim adjustment step">
          {STEP_PRESETS.map(preset => <Button key={preset.value} onClick={() => setStepMs(preset.value)} aria-label={`${preset.label} step ${preset.value} milliseconds`} aria-pressed={stepMs === preset.value} variant={stepMs === preset.value ? 'contained' : 'outlined'}>{preset.label}</Button>)}
        </ButtonGroup>
        <Typography variant="caption" sx={{color: 'text.disabled'}}>{stepMs === 100 ? '100 ms' : `${stepMs / 1000} second${stepMs === 1000 ? '' : 's'}`}</Typography>
      </Box>

      <Box sx={{display: 'flex', flexWrap: 'wrap', gap: {xs: 1.25, sm: 2.5}, mt: 1.25}}>
        <Endpoint label="Start" fieldId="cs-start-time" value={startPosition} onTimeChange={onStartChange} duration={duration} oppositeBound={endPosition} isStart onNudge={nudge} stepMs={stepMs} />
        <Endpoint label="End" fieldId="cs-end-time" value={endPosition} onTimeChange={onEndChange} duration={duration} oppositeBound={startPosition} isStart={false} onNudge={nudge} stepMs={stepMs} />
      </Box>
    </Box>
  )
}

function Timeline({label, detail, min, max, value, step, shiftStep, onChange, onCommit}) {
  return <Box className="cs-trim" sx={{position: 'relative'}}>
    <Box sx={{display: 'flex', justifyContent: 'space-between', gap: 1, mb: 0.25}}>
      <Typography variant="overline" sx={{color: '#ff7300', lineHeight: 1.4}}>{label}</Typography>
      <Typography variant="caption" sx={{color: 'text.secondary', fontFamily: 'var(--cs-mono-font)'}}>{detail}</Typography>
    </Box>
    <Slider min={min} max={max} step={step} shiftStep={shiftStep} value={value}
      onChange={onChange} onChangeCommitted={onCommit} valueLabelDisplay="auto" valueLabelFormat={millisToDuration}
      getAriaLabel={index => `${label}, ${index === 0 ? 'clip start' : 'clip end'}`}
      getAriaValueText={(v, index) => `${index === 0 ? 'Start' : 'End'} ${millisToDuration(v)}`}
      disableSwap
    />
  </Box>
}

function parseTimeToMillis(input) {
  if (input == null) return null
  const parts = String(input).trim().split(':')
  if (!parts[0] || parts.length > 3 || parts.some(part => part.trim() === '')) return null
  const nums = parts.map(p => Number(p))
  if (nums.some(n => !Number.isFinite(n) || n < 0)) return null
  let [hours, minutes, seconds] = [0, 0, 0]
  if (parts.length === 3) [hours, minutes, seconds] = nums
  else if (parts.length === 2) [minutes, seconds] = nums
  else [seconds] = nums
  if ((parts.length >= 2 && (minutes >= 60 || seconds >= 60))) return null
  return Math.round((hours * 3600 + minutes * 60 + seconds) * 1000)
}

function Endpoint({label, fieldId, value, onTimeChange, duration, oppositeBound, isStart, onNudge, stepMs}) {
  const [draft, setDraft] = useState(() => millisToDuration(value))
  const [rejected, setRejected] = useState(false)
  const [editing, setEditing] = useState(false)
  const valueRef = useRef(value)
  const previousValueRef = useRef(value)
  const escapeRef = useRef(false)
  useEffect(() => {
    const valueChanged = previousValueRef.current !== value
    valueRef.current = value
    previousValueRef.current = value
    if (!editing && valueChanged) {
      setDraft(millisToDuration(value))
      setRejected(false)
    }
  }, [value, editing])
  const commit = (input) => {
    const parsed = parseTimeToMillis(input)
    const clamped = parsed == null ? null : Math.max(0, Math.min(parsed, duration))
    const invalid = clamped == null || (isStart ? clamped > oppositeBound - MIN_GAP_MS : clamped < oppositeBound + MIN_GAP_MS)
    if (invalid) { setDraft(millisToDuration(valueRef.current)); setRejected(true); return }
    if (clamped !== valueRef.current) onTimeChange(clamped)
    setDraft(millisToDuration(clamped)); setRejected(false)
  }
  return <Box sx={{flex: {xs: '1 1 100%', sm: '1 1 200px'}, minWidth: 0}}>
    <Typography variant="overline" sx={{color: '#ff7300', display: 'block', mb: 0.5}}>{label}</Typography>
    <Box sx={{display: 'flex', gap: 0.75, alignItems: 'center'}}>
      <Button className="cs-trim-nudge" onClick={() => onNudge(isStart, -1)} disabled={isStart ? value - stepMs < 0 : value - stepMs < oppositeBound + MIN_GAP_MS} aria-label={`Move ${label.toLowerCase()} earlier by ${stepLabel(stepMs)}`}>−</Button>
      <TextField variant="outlined" size="small" fullWidth label="HH:MM:SS.mmm" id={fieldId}
        inputProps={{'aria-label': `${label} time as hours minutes seconds milliseconds`, 'aria-describedby': `${fieldId}-helper`, inputMode: 'decimal', spellCheck: false}}
        value={draft} onChange={e => { setDraft(e.target.value); setRejected(false) }} onFocus={() => setEditing(true)}
        onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); commit(e.target.value) } if (e.key === 'Escape') { escapeRef.current = true; setDraft(millisToDuration(valueRef.current)); setRejected(false); e.currentTarget.blur() } }}
        onBlur={e => { if (!escapeRef.current) commit(e.target.value); escapeRef.current = false; setEditing(false) }} error={rejected}
      />
      <Button className="cs-trim-nudge" onClick={() => onNudge(isStart, 1)} disabled={isStart ? value + stepMs > oppositeBound - MIN_GAP_MS : value + stepMs > duration} aria-label={`Move ${label.toLowerCase()} later by ${stepLabel(stepMs)}`}>+</Button>
    </Box>
    <FormHelperText id={`${fieldId}-helper`} sx={{minHeight: '1.25em', color: rejected ? '#ffb4a8' : 'text.disabled'}}>{rejected ? 'Invalid time or too close to other bound — restored' : 'Enter applies · Esc restores'}</FormHelperText>
  </Box>
}
