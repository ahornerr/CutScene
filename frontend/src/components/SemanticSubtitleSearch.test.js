import React from 'react'
import {act, fireEvent, render, screen, waitFor} from '@testing-library/react'
import SemanticSubtitleSearch from './SemanticSubtitleSearch'

const hit = {
  ratingKey: '42', mediaId: 8, partId: 9, title: 'The Plan', showTitle: 'Office Hours',
  season: 2, episode: 3, year: 2024, startMs: 65000, endMs: 69000,
  text: 'I think we need a plan.', similarity: .94,
}

afterEach(() => jest.restoreAllMocks())

test('searches, presents a dialogue match, and opens its workspace result', async () => {
  global.fetch = jest.fn().mockResolvedValue({ok: true, status: 200, json: () => Promise.resolve([hit])})
  const onOpenResult = jest.fn()
  render(<SemanticSubtitleSearch onOpenResult={onOpenResult}/>)

  fireEvent.change(screen.getByRole('textbox', {name: 'Search subtitles'}), {target: {value: 'need a plan'}})
  fireEvent.click(screen.getByRole('button', {name: 'Search'}))

  expect(await screen.findByText('I think we need a plan.', {exact: false})).toBeInTheDocument()
  expect(screen.getByText(/Office Hours\s+·\s+S2 · E3\s+·\s+2024/)).toBeInTheDocument()
  expect(global.fetch).toHaveBeenCalledWith('/subtitle-search?q=need%20a%20plan', expect.any(Object))
  fireEvent.click(screen.getByRole('button', {name: 'Open in workspace →'}))
  expect(onOpenResult).toHaveBeenCalledWith(hit)
})

test('shows an empty state and clears it', async () => {
  global.fetch = jest.fn().mockResolvedValue({ok: true, status: 200, json: () => Promise.resolve([])})
  render(<SemanticSubtitleSearch/>)
  fireEvent.change(screen.getByRole('textbox', {name: 'Search subtitles'}), {target: {value: 'quiet goodbye'}})
  fireEvent.submit(screen.getByRole('textbox', {name: 'Search subtitles'}).closest('form'))
  expect(await screen.findByText(/No subtitle moments found/)).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', {name: 'Clear search'}))
  await waitFor(() => expect(screen.queryByText(/No subtitle moments found/)).not.toBeInTheDocument())
})

test('renders subtitle-like markup as harmless normalized plain text', async () => {
  const maliciousHit = {...hit, text: '<i>Quiet</i> <img src=x onerror="window.bad=true"> <script>alert(1)</script> now'}
  global.fetch = jest.fn().mockResolvedValue({ok: true, status: 200, json: () => Promise.resolve([maliciousHit])})
  render(<SemanticSubtitleSearch/>)
  fireEvent.change(screen.getByRole('textbox', {name: 'Search subtitles'}), {target: {value: 'quiet'}})
  fireEvent.click(screen.getByRole('button', {name: 'Search'}))
  expect(await screen.findByText(/Quiet alert\(1\) now/)).toBeInTheDocument()
  expect(screen.queryByRole('img')).not.toBeInTheDocument()
  expect(document.querySelector('script')).toBeNull()
  expect(screen.queryByText(/<i>|<img|<script/)).not.toBeInTheDocument()
})

test('confirms whole-library indexing, polls progress, and shows completion', async () => {
  jest.useFakeTimers()
  global.fetch = jest.fn()
    .mockResolvedValueOnce({ok: false, status: 404, json: () => Promise.resolve({})})
    .mockResolvedValueOnce({ok: true, status: 202, json: () => Promise.resolve({id: 'index-1', state: 'running', progress: {discovered: 12, processed: 3, indexed: 2, skipped: 1, failed: 0, currentTitle: 'The Plan'}})})
    .mockResolvedValueOnce({ok: true, status: 200, json: () => Promise.resolve({id: 'index-1', state: 'completed', progress: {discovered: 12, processed: 12, indexed: 10, skipped: 2, failed: 0}})})
  render(<SemanticSubtitleSearch/>)

  fireEvent.click(screen.getByRole('button', {name: 'Index library'}))
  expect(screen.getByRole('heading', {name: 'Index the whole library?'})).toBeInTheDocument()
  fireEvent.click(screen.getByRole('button', {name: 'Start indexing'}))

  expect(await screen.findByText('Indexing: The Plan')).toBeInTheDocument()
  expect(global.fetch).toHaveBeenCalledWith('/subtitle-search/index-all', expect.objectContaining({method: 'POST'}))
  expect(screen.getByText('Index library').closest('button')).toBeDisabled()
  await act(async () => { jest.advanceTimersByTime(1500); await Promise.resolve() })
  expect(global.fetch).toHaveBeenCalledWith('/subtitle-search/index-jobs/index-1', expect.any(Object))
  expect(await screen.findByText('Library indexing is complete.')).toBeInTheDocument()
  jest.useRealTimers()
})

test('explains a resumed incremental index and separates unchanged from no-subtitle skips', async () => {
  global.fetch = jest.fn()
    .mockResolvedValueOnce({ok: false, status: 404, json: () => Promise.resolve({})})
    .mockResolvedValueOnce({ok: true, status: 202, json: () => Promise.resolve({
      id: 'resume-1', state: 'resuming', resumed: true,
      progress: {tracks_discovered: 20, tracks_processed: 8, tracks_indexed: 3, skipped_unchanged: 4, skipped_no_subtitles: 1, tracks_failed: 0, current_title: 'A New Episode'},
    })})
  render(<SemanticSubtitleSearch/>)
  fireEvent.click(screen.getByRole('button', {name: 'Index library'}))
  fireEvent.click(screen.getByRole('button', {name: 'Start indexing'}))

  expect(await screen.findByText('Resuming library index…')).toBeInTheDocument()
  expect(screen.getByText(/checks the library again and skips tracks that are already indexed and unchanged/)).toBeInTheDocument()
  expect(screen.getByText(/Discovered 20 · Processed 8 · Indexed 3 · Already indexed 4 · No subtitles 1 · Failed 0 tracks/)).toBeInTheDocument()
})

test('shows categorized source counters when the backend provides them', async () => {
  global.fetch = jest.fn().mockResolvedValue({ok: true, status: 200, json: () => Promise.resolve({
    id: 'categories-1', state: 'completed', progress: {
      sources_discovered: 30, processed_sources: 30, indexed_sources: 12,
      already_indexed_sources: 8, unsupported_sources: 4, empty_sources: 3, failed_sources: 3,
    },
  })})
  render(<SemanticSubtitleSearch/>)
  expect(await screen.findByText(/Discovered 30 · Processed 30 · Indexed 12 · Already indexed 8 · Unsupported 4 · Empty 3 · Failed 3 sources/)).toBeInTheDocument()
})

test('uses the API track and chunk counter names when they are present', async () => {
  global.fetch = jest.fn().mockResolvedValue({ok: true, status: 200, json: () => Promise.resolve({
    id: 'counter-1', state: 'completed', progress: {
      discovered: 10, processed: 10, indexedChunks: 24, unchangedTracks: 3,
      unsupportedTracks: 2, emptyTracks: 1, failed: 0,
    },
  })})
  render(<SemanticSubtitleSearch/>)
  expect(await screen.findByText(/Indexed chunks 24 · Unchanged tracks 3 · Unsupported tracks 2 · Empty tracks 1 · Failed 0 tracks/)).toBeInTheDocument()
})

test('does not treat owner-only indexing access as an expired login', async () => {
  const onAuthRequired = jest.fn()
  global.fetch = jest.fn().mockResolvedValue({ok: false, status: 403, json: () => Promise.resolve({})})
  render(<SemanticSubtitleSearch onAuthRequired={onAuthRequired}/>)
  expect(await screen.findByText('Library indexing is available to the server owner.')).toBeInTheDocument()
  expect(screen.queryByRole('button', {name: 'Index library'})).not.toBeInTheDocument()
  expect(onAuthRequired).not.toHaveBeenCalled()
})

test('restores a current job on return and resumes its polling', async () => {
  jest.useFakeTimers()
  global.fetch = jest.fn()
    .mockResolvedValueOnce({ok: true, status: 200, json: () => Promise.resolve({id: 'saved-1', state: 'running', progress: {discovered: 4, processed: 2, indexed: 2, currentTitle: 'Saved source'}})})
    .mockResolvedValueOnce({ok: true, status: 200, json: () => Promise.resolve({id: 'saved-1', state: 'completed', progress: {discovered: 4, processed: 4, indexed: 4}})})
  render(<SemanticSubtitleSearch/>)
  expect(await screen.findByText('Indexing: Saved source')).toBeInTheDocument()
  expect(screen.getByText('Index library').closest('button')).toBeDisabled()
  await act(async () => { jest.advanceTimersByTime(1500); await Promise.resolve() })
  expect(global.fetch).toHaveBeenCalledWith('/subtitle-search/index-jobs/saved-1', expect.any(Object))
  expect(await screen.findByText('Library indexing is complete.')).toBeInTheDocument()
  jest.useRealTimers()
})

test('uses current job status after an indexing conflict', async () => {
  global.fetch = jest.fn()
    .mockResolvedValueOnce({ok: false, status: 404, json: () => Promise.resolve({})})
    .mockResolvedValueOnce({ok: false, status: 409, json: () => Promise.resolve({})})
    .mockResolvedValueOnce({ok: true, status: 200, json: () => Promise.resolve({id: 'existing-1', state: 'running', progress: {discovered: 6, processed: 1, indexed: 1, currentTitle: 'Existing job'}})})
  render(<SemanticSubtitleSearch/>)
  fireEvent.click(screen.getByRole('button', {name: 'Index library'}))
  fireEvent.click(screen.getByRole('button', {name: 'Start indexing'}))
  expect(await screen.findByText('Indexing: Existing job')).toBeInTheDocument()
  expect(global.fetch).toHaveBeenCalledWith('/subtitle-search/index-jobs/current', expect.any(Object))
})
