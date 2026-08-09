import moment from "moment";

export function millisToDuration(millis) {
  const hours = Math.floor(millis / 1000 / 60 / 60)
  const minutes = Math.floor(millis / 1000 / 60) % 60
  const seconds = Math.floor(millis / 1000) % 60
  millis = millis % 1000
  if (millis === 0) {
    return String(hours).padStart(2, '0') + ':' + String(minutes).padStart(2, '0') + ':' + String(seconds).padStart(2, '0');
  } else {
    return String(hours).padStart(2, '0') + ':' + String(minutes).padStart(2, '0') + ':' + String(seconds).padStart(2, '0') + '.' + String(millis).padStart(3, '0');
  }
}

export function convertPositionToDate(position) {
  return moment().startOf('day').add(position, 'ms')
}

export function convertDateToPosition(date) {
  return date.diff(date.clone().startOf('day'))
}

export function isAbortError(error) {
  return error?.name === 'AbortError'
}

export function isPgsSubtitleStream(stream) {
  return String(stream?.type || '').toLowerCase() === 'pgs' || String(stream?.codec || '').toLowerCase().includes('pgs')
}

export const MIN_GAP_MS = 500
