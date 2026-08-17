// Keep format naming and ranking in one place so the picker and the default
// selection agree. Plex exposes codec reliably; `format` is accepted for
// compatible API responses too.
export function subtitleFormat(stream) {
  const value = String(stream?.format || stream?.codec || '').trim().toLowerCase()
  if (['srt', 'subrip'].includes(value)) return {label: 'SRT', rank: 0}
  if (['ass', 'ssa'].includes(value)) return {label: 'ASS/SSA', rank: 1}
  if (['webvtt', 'vtt'].includes(value)) return {label: 'WebVTT', rank: 2}
  if (['pgs', 'hdmv_pgs', 'hdmv-pgs'].includes(value) || String(stream?.type || '').toLowerCase() === 'pgs') return {label: 'PGS', rank: 5}
  if (String(stream?.type || '').toLowerCase() === 'text') return {label: value ? value.toUpperCase() : 'Text', rank: 3}
  return {label: value ? value.toUpperCase() : 'Unknown', rank: 4}
}

export function orderSubtitleStreams(streams) {
  return (Array.isArray(streams) ? streams : []).map((stream, position) => ({stream, position}))
    .sort((a, b) => subtitleFormat(a.stream).rank - subtitleFormat(b.stream).rank || a.position - b.position)
    .map(({stream}) => stream)
}
