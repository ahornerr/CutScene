import {Box, FormHelperText, Slider, TextField, Typography} from "@mui/material";
import {useEffect, useRef, useState} from "react";
import {millisToDuration, MIN_GAP_MS} from "../utils";

// Single dual-thumb timeline carrying the highlighted [start..end] selection
// region, plus two millisecond-capable Start/End time inputs. Each input
// accepts HH:MM:SS.mmm or HH:MM:SS — a single field per bound.
//
// The field is a controlled text input that holds the raw draft string while
// the user types. It syncs from the external numeric value when the user is
// not actively editing. On commit (Enter or blur), the draft is parsed
// strictly and clamped to [0, duration]; the 500ms minimum gap is enforced
// against the opposite bound. Invalid or gap-crossing commits are rejected —
// the field restores to the canonical formatted value with visible feedback.
//
// trimFlashKey increments when a subtitle selection sets the range, triggering
// a brief orange glow via the cs-trim-flash CSS animation.
export default function TrimScrubber({
  duration, startPosition, endPosition,
  onRangeChange, onStartChange, onEndChange,
  trimFlashKey = 0,
}) {
  const handleSlider = (_, next) => {
    const [s, e] = next
    if (e - s >= MIN_GAP_MS) onRangeChange(s, e)
  }

  if (startPosition == null || endPosition == null) {
    return (
      <Box className="cs-rise-2" sx={{mt: 2.5, height: 96, display: 'grid', placeItems: 'center'}}>
        <Typography variant="body2" sx={{color: 'text.secondary'}}>Preparing trim…</Typography>
      </Box>
    )
  }

  return (
    <Box className="cs-rise-2" sx={{mt: 2.5}}>
      <Box className="cs-trim" sx={{px: 1, position: 'relative'}}>
        {trimFlashKey > 0 && (
          <Box
            key={trimFlashKey}
            className="cs-trim-flash"
            sx={{position: 'absolute', inset: -4, borderRadius: 2, pointerEvents: 'none'}}
          />
        )}
        <Slider
          min={0}
          step={1}
          max={duration}
          value={[startPosition, endPosition]}
          onChange={handleSlider}
          valueLabelDisplay="auto"
          valueLabelFormat={millisToDuration}
          getAriaLabel={index => (index === 0 ? 'Clip start time' : 'Clip end time')}
          getAriaValueText={(value, index) => `${index === 0 ? 'Start' : 'End'} ${millisToDuration(value)}`}
          disableSwap
        />
      </Box>

      <Box sx={{display: 'flex', flexWrap: 'wrap', gap: 2.5, mt: 1}}>
        <BoundField
          label="Start"
          fieldId="cs-start-time"
          value={startPosition}
          onTimeChange={onStartChange}
          duration={duration}
          oppositeBound={endPosition}
          isStart
        />
        <BoundField
          label="End"
          fieldId="cs-end-time"
          value={endPosition}
          onTimeChange={onEndChange}
          duration={duration}
          oppositeBound={startPosition}
          isStart={false}
        />
      </Box>
    </Box>
  )
}

// Strict anchored timestamp parser. Accepts:
//   HH:MM:SS.mmm, HH:MM:SS, MM:SS.mmm, MM:SS, SS.mmm, SS
// Each segment must be a valid number. In multi-segment form, minutes and
// seconds must be < 60. No negative values. Returns null if unparseable.
function parseTimeToMillis(input) {
  if (input == null) return null
  const str = String(input).trim()
  if (str === '') return null
  const parts = str.split(':')
  if (parts.length > 3) return null
  const nums = parts.map(p => parseFloat(p))
  if (nums.some(n => isNaN(n))) return null
  let hours = 0, minutes = 0, seconds = 0
  if (parts.length === 3) {
    [hours, minutes, seconds] = nums
  } else if (parts.length === 2) {
    [minutes, seconds] = nums
  } else {
    [seconds] = nums
  }
  if (parts.length >= 2 && (minutes >= 60 || seconds >= 60)) return null
  if (hours < 0 || minutes < 0 || seconds < 0) return null
  const result = Math.round((hours * 3600 + minutes * 60 + seconds) * 1000)
  if (result < 0) return null
  return result
}

function BoundField({label, fieldId, value, onTimeChange, duration, oppositeBound, isStart}) {
  const [draft, setDraft] = useState(() => millisToDuration(value))
  const [rejected, setRejected] = useState(false)
  const [editing, setEditing] = useState(false)
  const valueRef = useRef(value)
  const previousValueRef = useRef(value)

  // Sync draft from external value when not actively editing. This handles
  // slider drags, subtitle clicks, and session changes that update the bound
  // outside the text field.
  useEffect(() => {
    const valueChanged = previousValueRef.current !== value
    valueRef.current = value
    previousValueRef.current = value
    if (!editing) {
      setDraft(millisToDuration(value))
      if (valueChanged) setRejected(false)
    }
  }, [value, editing])

  const commit = (input) => {
    const parsed = parseTimeToMillis(input)
    if (parsed == null) {
      setDraft(millisToDuration(valueRef.current))
      setRejected(true)
      return
    }

    // Clamp to [0, duration] only — not to the opposite bound. The gap check
    // is done separately so the error message can distinguish the two cases.
    let clamped = parsed
    if (duration != null && clamped > duration) clamped = duration
    if (clamped < 0) clamped = 0

    // Check minimum gap against the opposite bound.
    const gapViolated = isStart
      ? clamped > oppositeBound - MIN_GAP_MS
      : clamped < oppositeBound + MIN_GAP_MS

    if (gapViolated) {
      // Reject — restore canonical value, show gap error.
      setDraft(millisToDuration(valueRef.current))
      setRejected(true)
      return
    }

    // Accept.
    if (clamped !== valueRef.current) {
      onTimeChange(clamped)
    }
    setDraft(millisToDuration(clamped))
    setRejected(false)
  }

  return (
    <Box sx={{flex: '1 1 200px', minWidth: 200}}>
      <Typography variant="overline" sx={{color: '#ff7300', display: 'block', mb: 0.5}}>
        {label}
      </Typography>
      <TextField
        variant="outlined"
        size="small"
        fullWidth
        label="HH:MM:SS.mmm"
        id={fieldId}
        inputProps={{
          'aria-label': `${label} time as hours minutes seconds milliseconds`,
          inputMode: 'decimal',
          spellCheck: false,
          autoCapitalize: 'off',
          autoCorrect: 'off',
        }}
        value={draft}
        onChange={e => {
          setDraft(e.target.value)
          setRejected(false)
        }}
        onFocus={() => setEditing(true)}
        onKeyDown={e => {
          if (e.key === 'Enter') {
            e.preventDefault()
            commit(e.target.value)
          }
        }}
        onBlur={e => {
          commit(e.target.value)
          setEditing(false)
        }}
        error={rejected}
      />
      <FormHelperText sx={{minHeight: '1.25em', color: rejected ? '#ffb4a8' : 'text.disabled'}}>
        {rejected
          ? 'Invalid time or too close to other bound — restored'
          : `0 – ${millisToDuration(duration)}`}
      </FormHelperText>
    </Box>
  )
}
