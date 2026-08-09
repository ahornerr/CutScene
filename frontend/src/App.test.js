import React from 'react';
import {act, fireEvent, render, screen} from '@testing-library/react';
import App from './App';
import TrimScrubber from './components/TrimScrubber';
import {renderSubtitleMarkup, stripSubtitleMarkup} from './components/subtitle-markup';

const mockPlayerUrls = [];

jest.mock('react-player', () => {
  const React = require('react');
  return {
    __esModule: true,
    default: ({url, onError, onReady}) => {
      mockPlayerUrls.push(url);
      return React.createElement('div', { 'data-testid': 'react-player', 'data-url': url },
        React.createElement('button', {type: 'button', onClick: onError}, 'Mock player error'),
        React.createElement('button', {type: 'button', onClick: onReady}, 'Mock player ready'),
      );
    },
  };
});

const sessions = [
  {
    ratingKey: 'A', type: 'movie', title: 'Alpha', year: 2024, viewOffset: 0, duration: 120000,
    User: {title: 'viewer'}, Media: [{Part: [{id: '101'}]}],
  },
  {
    ratingKey: 'B', type: 'movie', title: 'Beta', year: 2024, viewOffset: 5000, duration: 120000,
    User: {title: 'viewer'}, Media: [{Part: [{id: '202'}]}],
  },
];

const response = (data, overrides = {}) => ({
  ok: true,
  status: 200,
  statusText: 'OK',
  json: () => Promise.resolve(data),
  ...overrides,
});

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return {promise, resolve, reject};
}

function installFetch(sessionsResponse = response(sessions)) {
  const pending = [];
  global.fetch = jest.fn((url, options = {}) => {
    if (url === '/sessions') return Promise.resolve(sessionsResponse);
    const request = deferred();
    pending.push({url, options, ...request});
    return request.promise;
  });
  return pending;
}

async function flush() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function resolveRequest(request, data) {
  await act(async () => {
    request.resolve(response(data));
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function resolveRequestWith(request, data, overrides = {}) {
  await act(async () => {
    request.resolve(response(data, overrides));
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function rejectRequest(request, error = new Error('network down')) {
  await act(async () => {
    request.reject(error);
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function advancePolling(ms = 2000) {
  await act(async () => {
    jest.advanceTimersByTime(ms);
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

function requestFor(pending, predicate, occurrence = 0) {
  const matches = pending.filter(request => predicate(request.url));
  if (!matches[occurrence]) {
    throw new Error(`Missing request ${occurrence}: ${pending.map(request => request.url).join(', ')}`);
  }
  return matches[occurrence];
}

async function selectSession(title) {
  fireEvent.click(screen.getByText(title, {exact: true}));
  await flush();
}

async function changeSession() {
  const buttons = screen.getAllByRole('button', {name: 'Change session'});
  fireEvent.click(buttons[0]);
  await flush();
}

async function selectSubtitle(label) {
  const select = screen.getByRole('combobox', {name: 'Subtitle track'});
  fireEvent.mouseDown(select);
  await flush();
  fireEvent.click(screen.getByRole('option', {name: label}));
  await flush();
}

async function selectAudioMode(label) {
  const select = screen.getByRole('combobox', {name: 'Audio mode'});
  fireEvent.mouseDown(select);
  await flush();
  const values = {
    'Standard stereo': 'standard',
    'Dialogue boost': 'dialogue',
    'Dialogue boost + normalize': 'dialogue_normalized',
  };
  const option = screen.getAllByRole('option')
    .find(candidate => candidate.getAttribute('data-value') === values[label]);
  expect(option).toBeDefined();
  fireEvent.click(option);
  await flush();
}

async function clickSubtitleOffset(direction) {
  // direction is 'later' (+) or 'earlier' (−); each click steps by 100ms.
  const label = direction === 'later' ? 'Subtitles later' : 'Subtitles earlier';
  fireEvent.click(screen.getByRole('button', {name: label}));
  await flush();
}

async function resetSubtitleOffset() {
  fireEvent.click(screen.getByRole('button', {name: 'Reset subtitle offset'}));
  await flush();
}

function textStreams() {
  return [
    {index: 0, type: 'text', codec: 'srt', displayTitle: 'X track'},
    {index: 1, type: 'text', codec: 'webvtt', displayTitle: 'Y track'},
  ];
}

afterEach(() => {
  jest.useRealTimers();
  jest.restoreAllMocks();
  mockPlayerUrls.length = 0;
});

beforeAll(() => {
  Element.prototype.scrollIntoView = jest.fn();
});

test('subtitle stream discovery sends the selected media part ID URL-encoded', async () => {
  const mediaId = 'part/101?segment=2';
  const selectedSessions = sessions.map(session => session.ratingKey === 'A'
    ? {...session, Media: [{Part: [{id: mediaId}]}]}
    : session
  );
  const pending = installFetch(response(selectedSessions));
  render(<App/>);
  await flush();

  await selectSession('Alpha');
  const streamsRequest = requestFor(pending, url => url.startsWith('/streams/A?mediaId='));
  expect(streamsRequest.url).toBe(`/streams/A?mediaId=${encodeURIComponent(mediaId)}`);
});

test('rapid session A to B aborts A stream work and stale A stream results cannot commit', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();

  await selectSession('Alpha');
  const aStreams = requestFor(pending, url => url === '/streams/A?mediaId=101');

  await changeSession();
  await selectSession('Beta');
  const bStreams = requestFor(pending, url => url === '/streams/B?mediaId=202');

  expect(aStreams.options.signal.aborted).toBe(true);

  await resolveRequest(aStreams, [{index: 0, type: 'text', codec: 'srt', displayTitle: 'A stale track'}]);
  await resolveRequest(bStreams, [{index: 0, type: 'text', codec: 'srt', displayTitle: 'B current track'}]);

  expect(screen.getByText('B current track')).toBeInTheDocument();
  expect(screen.queryByText('A stale track')).not.toBeInTheDocument();
});

test('session change aborts prewarm requests and stale prewarm results do not affect B', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();

  await selectSession('Alpha');
  const aStreams = requestFor(pending, url => url === '/streams/A?mediaId=101');
  await resolveRequest(aStreams, [
    {index: 0, type: 'text', codec: 'srt', displayTitle: 'Selected'},
    {index: 1, type: 'text', codec: 'ass', displayTitle: 'Prewarm A'},
    {index: 2, type: 'pgs', codec: 'pgssub', displayTitle: 'PGS A'},
  ]);

  const prewarm = requestFor(pending, url => url.includes('/subtitles/A?subtitle=1'));
  expect(pending.some(request => request.url.includes('/subtitles/A?subtitle=2'))).toBe(false);

  await changeSession();
  await selectSession('Beta');
  expect(aStreams.options.signal.aborted).toBe(true);
  expect(prewarm.options.signal.aborted).toBe(true);

  await resolveRequest(prewarm, [{start: 0, end: 1, text: 'stale prewarm'}]);
  expect(screen.queryByText('stale prewarm')).not.toBeInTheDocument();
});

test('switching subtitle X to Y aborts X without clearing Y loading or entries', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();

  await selectSession('Alpha');
  const streamsRequest = requestFor(pending, url => url === '/streams/A?mediaId=101');
  await resolveRequest(streamsRequest, textStreams());

  const xRequest = requestFor(pending, url => url.includes('/subtitles/A?subtitle=0'));
  await selectSubtitle('Y track');
  const yRequests = pending.filter(request => request.url.includes('/subtitles/A?subtitle=1'));
  const yRequest = yRequests[yRequests.length - 1];
  expect(yRequest).toBeDefined();
  expect(xRequest.options.signal.aborted).toBe(true);

  await resolveRequest(xRequest, [{start: 0, end: 1000, text: 'stale X'}]);
  expect(screen.getByText('Loading subtitles…')).toBeInTheDocument();

  await resolveRequest(yRequest, [{start: 1000, end: 2000, text: 'current Y'}]);
  expect(screen.getByText('current Y')).toBeInTheDocument();
  expect(screen.queryByText('stale X')).not.toBeInTheDocument();
});

test('prewarm requests only unselected text tracks and excludes PGS', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();

  await selectSession('Alpha');
  const streamsRequest = requestFor(pending, url => url === '/streams/A?mediaId=101');
  await resolveRequest(streamsRequest, [
    {index: 0, type: 'text', codec: 'srt', displayTitle: 'Selected'},
    {index: 1, type: 'text', codec: 'webvtt', displayTitle: 'Other text'},
    {index: 2, type: 'pgs', codec: 'pgssub', displayTitle: 'Bitmap'},
    {index: 3, type: 'text', codec: 'ass', displayTitle: 'Other ASS'},
  ]);

  expect(pending.some(request => request.url.includes('/subtitles/A?subtitle=1'))).toBe(true);
  expect(pending.some(request => request.url.includes('/subtitles/A?subtitle=3'))).toBe(true);
  expect(pending.some(request => request.url.includes('/subtitles/A?subtitle=2'))).toBe(false);
});

test('initial preview resolves subtitle state once, then refreshes on intentional changes', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();

  // Session select waits for the initial stream choice instead of starting a
  // no-subtitle preview that is immediately replaced.
  await selectSession('Alpha');
  expect(screen.queryByTestId('react-player')).not.toBeInTheDocument();
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());
  expect(mockPlayerUrls).toEqual([
    '/preview/A/00:00:00/00:01:00?mediaId=101&subtitle=0',
  ]);
  expect(screen.getByTestId('react-player').getAttribute('data-url')).toContain('subtitle=0');
  expect(screen.queryByRole('button', {name: 'Preview selection'})).not.toBeInTheDocument();

  // Changing session does not generate stale A previews.
  const beforeB = mockPlayerUrls.length;
  await changeSession();
  await selectSession('Beta');
  expect(mockPlayerUrls.slice(beforeB).some(url => url.includes('/preview/A/'))).toBe(false);

  // Resolve B streams — the initial subtitle track applies one preview.
  await resolveRequest(requestFor(pending, url => url === '/streams/B?mediaId=202'), textStreams());
  expect(screen.getByTestId('react-player').getAttribute('data-url')).toContain('subtitle=0');
  expect(screen.queryByRole('button', {name: 'Preview selection'})).not.toBeInTheDocument();

  // Switching subtitle track auto-applies immediately (no debounce).
  await selectSubtitle('Y track');
  expect(screen.getByTestId('react-player').getAttribute('data-url')).toContain('subtitle=1');

  // Committing a typed Start timestamp is a discrete, intentional action —
  // it auto-applies the preview exactly once (like a subtitle pick), instead
  // of deferring behind the stale "Preview selection" button.
  const urlsBeforeStartCommit = mockPlayerUrls.length;
  const startTime = screen.getByRole('textbox', {name: 'Start time as hours minutes seconds milliseconds'});
  fireEvent.focus(startTime);
  // Intermediate invalid text does NOT refresh the preview while typing.
  fireEvent.change(startTime, {target: {value: '00:'}});
  await flush();
  expect(mockPlayerUrls.length).toBe(urlsBeforeStartCommit);
  // Committing a valid Start rebuilds the preview URL exactly once.
  fireEvent.change(startTime, {target: {value: '00:00:05.500'}});
  fireEvent.blur(startTime);
  await flush();
  expect(mockPlayerUrls.length).toBe(urlsBeforeStartCommit + 1);
  // Session B starts at 5000ms; committing 5500ms → 00:00:05.500.
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toBe('/preview/B/00:00:05.500/00:01:05?mediaId=202&subtitle=1');
  expect(screen.queryByRole('button', {name: 'Preview selection'})).not.toBeInTheDocument();

  // Committing a typed End timestamp likewise auto-applies exactly once.
  const urlsBeforeEndCommit = mockPlayerUrls.length;
  const endTime = screen.getByRole('textbox', {name: 'End time as hours minutes seconds milliseconds'});
  fireEvent.focus(endTime);
  fireEvent.change(endTime, {target: {value: '00:00:10.000'}});
  fireEvent.blur(endTime);
  await flush();
  expect(mockPlayerUrls.length).toBe(urlsBeforeEndCommit + 1);
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toBe('/preview/B/00:00:05.500/00:00:10?mediaId=202&subtitle=1');
  expect(screen.queryByRole('button', {name: 'Preview selection'})).not.toBeInTheDocument();

  // Committing an invalid timestamp restores the field and does NOT refresh.
  const urlsBeforeInvalid = mockPlayerUrls.length;
  fireEvent.focus(endTime);
  fireEvent.change(endTime, {target: {value: 'not-a-time'}});
  fireEvent.blur(endTime);
  await flush();
  expect(mockPlayerUrls.length).toBe(urlsBeforeInvalid);
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toBe('/preview/B/00:00:05.500/00:00:10?mediaId=202&subtitle=1');
  expect(endTime).toHaveValue('00:00:10');

  // Committing a value that violates the minimum gap is rejected and does NOT
  // refresh the preview.
  const urlsBeforeGap = mockPlayerUrls.length;
  fireEvent.focus(endTime);
  fireEvent.change(endTime, {target: {value: '00:00:05.700'}}); // 5700ms, within 500ms of 5500
  fireEvent.blur(endTime);
  await flush();
  expect(mockPlayerUrls.length).toBe(urlsBeforeGap);
  expect(endTime).toHaveValue('00:00:10');
  expect(screen.getByText(/too close to other bound/)).toBeInTheDocument();
});

test('slider drag defers preview behind the stale button; explicit apply refreshes', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());

  // Initial preview reflects 00:00:00–00:01:00.
  const urlBefore = screen.getByTestId('react-player').getAttribute('data-url');
  expect(urlBefore).toBe('/preview/A/00:00:00/00:01:00?mediaId=101&subtitle=0');

  // A slider nudge is a continuous adjustment — it marks the preview stale
  // and does NOT auto-reload the player (the URL stays the same).
  const startInput = screen.getByRole('slider', {name: 'Clip start time'});
  fireEvent.change(startInput, {target: {value: '1000'}});
  await flush();
  expect(screen.getByRole('button', {name: 'Preview selection'})).toBeInTheDocument();
  expect(screen.getByTestId('react-player').getAttribute('data-url')).toBe(urlBefore);
  // The slider thumb moved (startPosition updated) even though the player URL
  // did not — the change is held until the user applies it.
  expect(startInput).toHaveValue('1000');

  // Explicit "Preview selection" applies the new bounds and clears stale.
  fireEvent.click(screen.getByRole('button', {name: 'Preview selection'}));
  await flush();
  // Start moved to 1000ms → 00:00:01.
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toBe('/preview/A/00:00:01/00:01:00?mediaId=101&subtitle=0');
  expect(screen.queryByRole('button', {name: 'Preview selection'})).not.toBeInTheDocument();
});

test('dialogue audio mode updates the preview URL and render payload', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());

  await selectAudioMode('Dialogue boost');
  expect(screen.getByTestId('react-player').getAttribute('data-url')).toBe(
    '/preview/A/00:00:00/00:01:00?mediaId=101&subtitle=0&audioMode=dialogue'
  );

  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const createRequest = requestFor(pending, url => url === '/render-jobs');
  expect(JSON.parse(createRequest.options.body)).toMatchObject({audioMode: 'dialogue'});
});

test('changing sessions resets audio mode to standard', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());
  await selectAudioMode('Dialogue boost');
  expect(screen.getByTestId('react-player').getAttribute('data-url')).toContain('audioMode=dialogue');

  await changeSession();
  await selectSession('Beta');
  expect(screen.getByRole('combobox', {name: 'Audio mode'})).toHaveTextContent('Standard stereo');
  await resolveRequest(requestFor(pending, url => url === '/streams/B?mediaId=202'), textStreams());
  expect(screen.getByTestId('react-player').getAttribute('data-url')).toBe(
    '/preview/B/00:00:05/00:01:05?mediaId=202&subtitle=0'
  );
});

test('positive subtitle offset delays subtitles: preview URL and render payload carry subtitleOffsetMs', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());

  // Default offset is 0 — the preview URL omits the param entirely.
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toBe('/preview/A/00:00:00/00:01:00?mediaId=101&subtitle=0');
  expect(screen.getByRole('button', {name: 'Reset subtitle offset'})).toBeDisabled();

  // Two +100ms steps → +200ms. The preview URL gains subtitleOffsetMs=200.
  await clickSubtitleOffset('later');
  await clickSubtitleOffset('later');
  expect(screen.getByRole('button', {name: 'Reset subtitle offset'})).toHaveTextContent('+200 ms');
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toBe('/preview/A/00:00:00/00:01:00?mediaId=101&subtitle=0&subtitleOffsetMs=200');
  expect(screen.queryByRole('button', {name: 'Preview selection'})).not.toBeInTheDocument();

  // The render-job payload carries subtitleOffsetMs alongside the other fields.
  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const createRequest = requestFor(pending, url => url === '/render-jobs');
  expect(JSON.parse(createRequest.options.body)).toMatchObject({
    ratingKey: 'A', mediaId: 101, subtitleIndex: 0, audioMode: 'standard', subtitleOffsetMs: 200,
  });
});

test('negative subtitle offset advances subtitles and renders an earlier preview param', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());

  await clickSubtitleOffset('earlier');
  expect(screen.getByRole('button', {name: 'Reset subtitle offset'})).toHaveTextContent('−100 ms');
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toBe('/preview/A/00:00:00/00:01:00?mediaId=101&subtitle=0&subtitleOffsetMs=-100');

  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const createRequest = requestFor(pending, url => url === '/render-jobs');
  expect(JSON.parse(createRequest.options.body)).toMatchObject({subtitleOffsetMs: -100});
});

test('subtitle offset is retained while selecting subtitle tracks and entries', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());

  await clickSubtitleOffset('later');
  await clickSubtitleOffset('later');
  await clickSubtitleOffset('later'); // +300ms
  expect(screen.getByRole('button', {name: 'Reset subtitle offset'})).toHaveTextContent('+300 ms');

  // Switching the subtitle track keeps the offset.
  await selectSubtitle('Y track');
  expect(screen.getByRole('button', {name: 'Reset subtitle offset'})).toHaveTextContent('+300 ms');
  const yRequest = pending.filter(r => r.url.includes('/subtitles/A?subtitle=1')).slice(-1)[0];
  await resolveRequest(yRequest, [{start: 1000, end: 2000, text: 'Y one'}]);

  // Picking a subtitle entry keeps the offset too — it must not reset.
  fireEvent.click(screen.getByRole('button', {name: /Subtitle at 00:00:01/}));
  await flush();
  expect(screen.getByRole('button', {name: 'Reset subtitle offset'})).toHaveTextContent('+300 ms');
});

test('subtitle offset shifts the clip range derived from a subtitle pick', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());
  const subtitleRequest = requestFor(pending, url => url.includes('/subtitles/A?subtitle=0'));
  await resolveRequest(subtitleRequest, [
    {start: 5000, end: 6000, text: 'Range set'},
  ]);

  // +500ms offset: the subtitle displays at 5500–6500 in the output, so the
  // clip range (with 500ms padding) becomes 5000–7000 instead of 4500–6500.
  for (let i = 0; i < 5; i++) await clickSubtitleOffset('later');
  expect(screen.getByRole('button', {name: 'Reset subtitle offset'})).toHaveTextContent('+500 ms');

  fireEvent.click(screen.getByRole('button', {name: /Subtitle at 00:00:05/}));
  await flush();
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toContain('/preview/A/00:00:05/00:00:07');
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toContain('subtitleOffsetMs=500');
});

test('clicking the offset value resets to 0 and drops the preview param', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());

  await clickSubtitleOffset('later');
  await clickSubtitleOffset('later');
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toContain('subtitleOffsetMs=200');

  await resetSubtitleOffset();
  expect(screen.getByRole('button', {name: 'Reset subtitle offset'})).toHaveTextContent('0 ms');
  expect(screen.getByRole('button', {name: 'Reset subtitle offset'})).toBeDisabled();
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toBe('/preview/A/00:00:00/00:01:00?mediaId=101&subtitle=0');
});

test('changing sessions resets the subtitle offset to 0', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());
  await clickSubtitleOffset('later');
  expect(screen.getByRole('button', {name: 'Reset subtitle offset'})).toHaveTextContent('+100 ms');

  await changeSession();
  await selectSession('Beta');
  await resolveRequest(requestFor(pending, url => url === '/streams/B?mediaId=202'), textStreams());
  expect(screen.getByRole('button', {name: 'Reset subtitle offset'})).toHaveTextContent('0 ms');
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toBe('/preview/B/00:00:05/00:01:05?mediaId=202&subtitle=0');
});

test('subtitle offset control is disabled when no subtitle track is selected', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), []);

  expect(screen.getByRole('button', {name: 'Subtitles later'})).toBeDisabled();
  expect(screen.getByRole('button', {name: 'Subtitles earlier'})).toBeDisabled();
  // No subtitle param in the preview URL when offset is irrelevant.
  expect(screen.getByTestId('react-player').getAttribute('data-url')).not.toContain('subtitleOffsetMs');
});

test('changing offset refits an active single subtitle selection around shifted timings', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());
  const subtitleRequest = requestFor(pending, url => url.includes('/subtitles/A?subtitle=0'));
  await resolveRequest(subtitleRequest, [{start: 5000, end: 6000, text: 'Solo'}]);

  // Picking the entry sets the range to 4500–6500 (500ms padding).
  fireEvent.click(screen.getByRole('button', {name: /Subtitle at 00:00:05/}));
  await flush();
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toContain('/preview/A/00:00:04.500/00:00:06.500');

  // Changing the offset refits the active selection without re-picking.
  // +200ms → shifted 5200–6200 → padded 4700–6700.
  await clickSubtitleOffset('later');
  await clickSubtitleOffset('later');
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toContain('/preview/A/00:00:04.700/00:00:06.700');
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toContain('subtitleOffsetMs=200');
  // Auto-applied — no stale button.
  expect(screen.queryByRole('button', {name: 'Preview selection'})).not.toBeInTheDocument();
});

test('changing offset refits an active contiguous multi-selection around the full shifted span', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());
  const subtitleRequest = requestFor(pending, url => url.includes('/subtitles/A?subtitle=0'));
  await resolveRequest(subtitleRequest, [
    {start: 5000, end: 6000, text: 'A'},
    {start: 7000, end: 8000, text: 'B'},
    {start: 9000, end: 10000, text: 'C'},
  ]);

  // Single pick on the first entry → 4500–6500.
  fireEvent.click(screen.getByRole('button', {name: /Subtitle at 00:00:05: A/}));
  await flush();
  // Shift+click the third entry extends the selection to entries 0–2.
  fireEvent.click(screen.getByRole('button', {name: /Subtitle at 00:00:09: C/}), {shiftKey: true});
  await flush();
  // Span 5000–10000 → padded 4500–10500.
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toContain('/preview/A/00:00:04.500/00:00:10.500');

  // +500ms refits the whole selection: shifted 5500–10500 → padded 5000–11000.
  for (let i = 0; i < 5; i++) await clickSubtitleOffset('later');
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toContain('/preview/A/00:00:05/00:00:11');
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toContain('subtitleOffsetMs=500');
  expect(screen.queryByRole('button', {name: 'Preview selection'})).not.toBeInTheDocument();
});

test('changing offset does not alter a manually-set range when no subtitle selection is active', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());
  // Subtitles load but nothing is picked — anchor stays -1.
  await resolveRequest(
    requestFor(pending, url => url.includes('/subtitles/A?subtitle=0')),
    [{start: 5000, end: 6000, text: 'Unpicked'}]
  );

  // Initial manual range is 00:00:00–00:01:00.
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toBe('/preview/A/00:00:00/00:01:00?mediaId=101&subtitle=0');

  // The offset still reaches the preview URL, but the range is untouched.
  await clickSubtitleOffset('later');
  await clickSubtitleOffset('later');
  expect(screen.getByTestId('react-player').getAttribute('data-url'))
    .toBe('/preview/A/00:00:00/00:01:00?mediaId=101&subtitle=0&subtitleOffsetMs=200');
  expect(screen.getByRole('slider', {name: 'Clip start time'})).toHaveValue('0');
  expect(screen.getByRole('slider', {name: 'Clip end time'})).toHaveValue('60000');
  expect(screen.queryByRole('button', {name: 'Preview selection'})).not.toBeInTheDocument();
});

test('subtitle entry click sets clip range and auto-applies preview', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();

  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());
  const xRequest = requestFor(pending, url => url.includes('/subtitles/A?subtitle=0'));
  await resolveRequest(xRequest, [
    {start: 5000, end: 6000, text: 'Range set'},
  ]);

  // Before clicking, preview reflects initial 00:00:00–00:01:00 bounds.
  expect(screen.getByTestId('react-player').getAttribute('data-url')).toContain('/preview/A/00:00:00/00:01:00');

  // Clicking a subtitle entry sets the clip range and auto-applies preview.
  fireEvent.click(screen.getByRole('button', {name: /Subtitle at 00:00:05/}));
  await flush();

  // Preview now reflects the subtitle-derived range (500ms padding).
  expect(screen.getByTestId('react-player').getAttribute('data-url')).toContain('/preview/A/00:00:04.500/00:00:06.500');
  // Preview is not stale — it was auto-applied.
  expect(screen.queryByRole('button', {name: 'Preview selection'})).not.toBeInTheDocument();
});

test('login CTA targets the Plex auth endpoint for opaque redirects', async () => {
  installFetch(response(null, {type: 'opaqueredirect', ok: false}));
  render(<App/>);
  await flush();

  expect(screen.getByRole('link', {name: 'Log in with Plex'})).toHaveAttribute('href', '/authUrl');
});

test('session loading exposes a live status message', async () => {
  const sessionsRequest = deferred();
  installFetch(sessionsRequest.promise);
  render(<App/>);

  expect(screen.getByRole('status')).toHaveTextContent('Loading sessions…');

  await act(async () => {
    sessionsRequest.resolve(response(sessions));
    await Promise.resolve();
    await Promise.resolve();
  });
  expect(screen.getByText('Alpha')).toBeInTheDocument();
});

test('streams loading is shown before the empty-stream state', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');

  expect(screen.getByText('Loading subtitle tracks…')).toBeInTheDocument();
  expect(screen.queryByText('No subtitle tracks available for this session.')).not.toBeInTheDocument();

  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), []);
  expect(screen.getByText('No subtitle tracks available for this session.')).toBeInTheDocument();
});

test('changing subtitle tracks clears stale range selection and exposes selected state', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');

  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());
  const xRequest = requestFor(pending, url => url.includes('/subtitles/A?subtitle=0'));
  await resolveRequest(xRequest, [
    {start: 1000, end: 2000, text: 'X one'},
    {start: 3000, end: 4000, text: 'X two'},
  ]);

  const xRow = screen.getByRole('button', {name: 'Subtitle at 00:00:01: X one'});
  expect(xRow).not.toHaveAttribute('aria-selected');
  expect(xRow).toHaveAttribute('aria-pressed', 'false');
  fireEvent.click(xRow);
  expect(screen.getByRole('button', {name: 'Subtitle at 00:00:01: X one, selected'}))
    .toHaveAttribute('aria-pressed', 'true');

  await selectSubtitle('Y track');
  const yRequests = pending.filter(request => request.url.includes('/subtitles/A?subtitle=1'));
  const yRequest = yRequests[yRequests.length - 1];
  await resolveRequest(yRequest, [{start: 5000, end: 6000, text: 'Y one'}]);

  const yRow = screen.getByRole('button', {name: 'Subtitle at 00:00:05: Y one'});
  expect(yRow).not.toHaveAttribute('aria-selected');
  expect(yRow).toHaveAttribute('aria-pressed', 'false');
  expect(screen.getByText(/sets clip range/)).toBeInTheDocument();
  expect(screen.queryByRole('button', {name: 'Subtitle at 00:00:01: X one, selected'}))
    .not.toBeInTheDocument();
});

test('player error clears when the replacement preview reports ready', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), []);

  fireEvent.click(screen.getByRole('button', {name: 'Mock player error'}));
  expect(screen.getByRole('alert')).toHaveTextContent('Preview failed to load');

  fireEvent.click(screen.getByRole('button', {name: 'Mock player ready'}));
  expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  expect(pending.some(request => request.url === '/streams/A?mediaId=101')).toBe(true);
});

test('render-job POST body and queued/running/succeeded polling produce a sanitized download', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const createRequest = requestFor(pending, url => url === '/render-jobs');
  expect(createRequest.options.method).toBe('POST');
  expect(createRequest.options.headers['Content-Type']).toBe('application/json');
  expect(JSON.parse(createRequest.options.body)).toEqual({
    ratingKey: 'A', mediaId: 101, fromMs: 0, toMs: 60000, subtitleIndex: -1, audioMode: 'standard',
    subtitleOffsetMs: 0,
  });

  await resolveRequestWith(createRequest, {id: 'job-1', status: 'queued'}, {status: 202});
  let poll = requestFor(pending, url => url === '/render-jobs/job-1');
  await resolveRequest(poll, {id: 'job-1', status: 'queued'});
  expect(screen.getByText('Queued')).toBeInTheDocument();
  expect(screen.getByText(/Render job status: Queued/)).toHaveAttribute('aria-live', 'polite');

  await advancePolling();
  poll = pending.filter(request => request.url === '/render-jobs/job-1').slice(-1)[0];
  await resolveRequest(poll, {id: 'job-1', status: 'running'});
  expect(screen.getByText('Rendering')).toBeInTheDocument();
  expect(screen.getByText(/Render job status: Rendering/)).toHaveAttribute('aria-live', 'polite');

  await advancePolling();
  poll = pending.filter(request => request.url === '/render-jobs/job-1').slice(-1)[0];
  const expiresAt = new Date(Date.now() + 1500).toISOString();
  await resolveRequest(poll, {
    id: 'job-1', status: 'succeeded', downloadUrl: '/render-jobs/job-1/download', expiresAt,
  });
  expect(screen.getByRole('link', {name: 'Download clip'})).toHaveAttribute(
    'href', '/render-jobs/job-1/download'
  );
  expect(screen.getByText(/Render job status: Ready\. Download ready\./))
    .toHaveAttribute('aria-live', 'polite');

  fireEvent.click(screen.getByRole('link', {name: 'Download clip'}));
  expect(screen.getByText(/Download started/)).toBeInTheDocument();

  await advancePolling(1000);
  expect(screen.getByRole('link', {name: 'Download clip'})).toBeInTheDocument();
  await advancePolling(1000);
  expect(screen.getByText('Expired')).toBeInTheDocument();
  expect(screen.queryByRole('link', {name: 'Download clip'})).not.toBeInTheDocument();
});

test('invalid media IDs fail clearly without posting an invalid render payload', async () => {
  const invalidSessions = sessions.map(session => session.ratingKey === 'A'
    ? {...session, Media: [{Part: [{id: '101.5'}]}]}
    : session
  );
  const pending = installFetch(response(invalidSessions));
  render(<App/>);
  await flush();
  await selectSession('Alpha');

  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));

  expect(pending.some(request => request.url === '/render-jobs')).toBe(false);
  expect(screen.getByRole('alert')).toHaveTextContent('invalid media ID');
});

test('render jobs keep an immutable submitted spec separate from edited controls', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());
  await selectAudioMode('Dialogue boost');

  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const createRequest = requestFor(pending, url => url === '/render-jobs');
  expect(JSON.parse(createRequest.options.body)).toMatchObject({
    fromMs: 0, toMs: 60000, subtitleIndex: 0, audioMode: 'dialogue',
  });
  expect(screen.getByText('Submitted clip')).toBeInTheDocument();
  expect(screen.getByText('00:00:00 – 00:01:00')).toBeInTheDocument();
  expect(screen.getAllByText('X track')).toHaveLength(2);

  const end = screen.getByRole('textbox', {name: 'End time as hours minutes seconds milliseconds'});
  fireEvent.change(end, {target: {value: '00:00:30.000'}});
  fireEvent.blur(end);
  await flush();

  // The live controls change, but the submitted output description does not.
  expect(screen.getByText('00:00:00 – 00:01:00')).toBeInTheDocument();
  expect(screen.getByText('Current selection').parentElement).toHaveTextContent('00:00:30');
  expect(screen.queryByText('Your selection changed since the last render')).not.toBeInTheDocument();
});

test('retryable render failures resubmit the immutable full submitted payload', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());
  await selectAudioMode('Dialogue boost');

  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const firstCreate = requestFor(pending, url => url === '/render-jobs');
  const submittedBody = JSON.parse(firstCreate.options.body);
  expect(submittedBody).toEqual({
    ratingKey: 'A', mediaId: 101, fromMs: 0, toMs: 60000, subtitleIndex: 0, audioMode: 'dialogue',
    subtitleOffsetMs: 0,
  });

  await resolveRequestWith(firstCreate, {id: 'job-retry', status: 'queued'}, {status: 202});
  const poll = requestFor(pending, url => url === '/render-jobs/job-retry');

  // Change the live controls before the server reports failure.
  const end = screen.getByRole('textbox', {name: 'End time as hours minutes seconds milliseconds'});
  fireEvent.focus(end);
  fireEvent.change(end, {target: {value: '00:00:30.000'}});
  fireEvent.blur(end);
  await selectAudioMode('Standard stereo');
  await flush();

  await resolveRequest(poll, {
    id: 'job-retry', status: 'failed',
    error: {code: 'source_unavailable', message: 'Source unavailable.', retryable: true},
  });
  fireEvent.click(screen.getByRole('button', {name: 'Try again'}));

  const secondCreate = requestFor(pending, url => url === '/render-jobs', 1);
  expect(secondCreate.options.method).toBe('POST');
  expect(secondCreate.options.headers['Content-Type']).toBe('application/json');
  expect(JSON.parse(secondCreate.options.body)).toEqual(submittedBody);
  expect(JSON.parse(secondCreate.options.body)).toMatchObject({mediaId: 101, ratingKey: 'A'});
});

test('nonretryable render failures do not resubmit a job', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const createRequest = requestFor(pending, url => url === '/render-jobs');
  await resolveRequestWith(createRequest, {id: 'job-no-retry', status: 'queued'}, {status: 202});
  const poll = requestFor(pending, url => url === '/render-jobs/job-no-retry');
  await resolveRequest(poll, {
    id: 'job-no-retry', status: 'failed',
    error: {code: 'validation_error', message: 'The clip is invalid.', retryable: false},
  });

  expect(screen.getByRole('button', {name: 'Start new render'})).toBeInTheDocument();
  expect(screen.queryByRole('button', {name: 'Try again'})).not.toBeInTheDocument();
  expect(pending.filter(request => request.url === '/render-jobs')).toHaveLength(1);
});

test('polling transport failures retry status for the same job without creating a new job', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const createRequest = requestFor(pending, url => url === '/render-jobs');
  await resolveRequestWith(createRequest, {id: 'job-transport', status: 'queued'}, {status: 202});
  const firstPoll = requestFor(pending, url => url === '/render-jobs/job-transport');

  await rejectRequest(firstPoll);
  expect(screen.getByRole('status')).toHaveTextContent(/Connection issue.*Retrying in 4s…/);
  expect(screen.getByRole('button', {name: 'Queued…'})).toBeDisabled();

  await advancePolling(3999);
  expect(pending.filter(request => request.url === '/render-jobs/job-transport')).toHaveLength(1);
  await advancePolling(1);
  const secondPoll = requestFor(pending, url => url === '/render-jobs/job-transport', 1);
  expect(pending.filter(request => request.url === '/render-jobs')).toHaveLength(1);

  await resolveRequest(secondPoll, {
    id: 'job-transport', status: 'succeeded', downloadUrl: '/render-jobs/job-transport/download',
  });
  expect(screen.getByRole('link', {name: 'Download clip'})).toBeInTheDocument();
});

test('visible Retry now re-fetches the same job after a polling transport failure', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const createRequest = requestFor(pending, url => url === '/render-jobs');
  await resolveRequestWith(createRequest, {id: 'job-retry-poll', status: 'queued'}, {status: 202});
  const firstPoll = requestFor(pending, url => url === '/render-jobs/job-retry-poll');

  await resolveRequestWith(firstPoll, {}, {status: 401, ok: false});
  expect(screen.getByRole('button', {name: 'Retry now'})).toBeInTheDocument();
  expect(screen.getByRole('alert')).toHaveTextContent('Authentication expired');

  fireEvent.click(screen.getByRole('button', {name: 'Retry now'}));
  const secondPoll = requestFor(pending, url => url === '/render-jobs/job-retry-poll', 1);
  expect(pending.filter(request => request.url === '/render-jobs')).toHaveLength(1);

  await resolveRequest(secondPoll, {id: 'job-retry-poll', status: 'queued'});
  expect(screen.getByText('Queued')).toBeInTheDocument();
  expect(screen.getByText(/Render job status: Queued/)).toHaveAttribute('aria-live', 'polite');
});

test('Retry now after Retry-After on job creation resubmits the immutable spec', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const firstCreate = requestFor(pending, url => url === '/render-jobs');
  const submittedBody = JSON.parse(firstCreate.options.body);

  await resolveRequestWith(firstCreate, {}, {
    status: 503,
    ok: false,
    headers: {get: name => name === 'Retry-After' ? '3' : null},
  });
  expect(screen.getByRole('button', {name: 'Retry now'})).toBeInTheDocument();
  expect(screen.getByRole('alert')).toHaveTextContent('The render queue is busy');

  fireEvent.click(screen.getByRole('button', {name: 'Retry now'}));
  const secondCreate = requestFor(pending, url => url === '/render-jobs', 1);
  expect(secondCreate.options.method).toBe('POST');
  expect(JSON.parse(secondCreate.options.body)).toEqual(submittedBody);
});

test('polling honors Retry-After before rechecking the same job', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const createRequest = requestFor(pending, url => url === '/render-jobs');
  await resolveRequestWith(createRequest, {id: 'job-backoff', status: 'queued'}, {status: 202});
  const firstPoll = requestFor(pending, url => url === '/render-jobs/job-backoff');

  await resolveRequestWith(firstPoll, {}, {
    status: 503,
    ok: false,
    headers: {get: name => name === 'Retry-After' ? '3' : null},
  });
  expect(screen.getByRole('status')).toHaveTextContent('Retrying in 3s…');

  await advancePolling(2999);
  expect(pending.filter(request => request.url === '/render-jobs/job-backoff')).toHaveLength(1);
  await advancePolling(1);
  expect(pending.filter(request => request.url === '/render-jobs/job-backoff')).toHaveLength(2);
});

test('failed render jobs expose only sanitized retry information', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const createRequest = requestFor(pending, url => url === '/render-jobs');
  await resolveRequestWith(createRequest, {id: 'job-failed', status: 'queued'}, {status: 202});
  const poll = requestFor(pending, url => url === '/render-jobs/job-failed');
  await resolveRequest(poll, {
    id: 'job-failed',
    status: 'failed',
    error: {
      code: 'source_unavailable',
      message: 'The source media is unavailable.',
      retryable: true,
    },
  });

  expect(screen.getByRole('alert')).toHaveTextContent('The source media is unavailable.');
  expect(screen.getByRole('button', {name: 'Try again'})).toBeInTheDocument();
  expect(screen.queryByText(/X-Plex-Token|stderr|https?:\/\//i)).not.toBeInTheDocument();

  fireEvent.click(screen.getByRole('button', {name: 'Try again'}));
  expect(pending.filter(request => request.url === '/render-jobs')).toHaveLength(2);
});

test('expired render jobs stop polling and offer a fresh render', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const createRequest = requestFor(pending, url => url === '/render-jobs');
  await resolveRequestWith(createRequest, {id: 'job-expired', status: 'queued'}, {status: 202});
  const poll = requestFor(pending, url => url === '/render-jobs/job-expired');
  await resolveRequestWith(poll, {error: {code: 'render_expired', message: 'render job has expired'}}, {status: 410, ok: false});

  expect(screen.getByText('Expired')).toBeInTheDocument();
  expect(screen.getByRole('button', {name: 'Render again'})).toBeInTheDocument();
  expect(screen.getByRole('status')).toHaveTextContent('The rendered clip expired. Render again to download.');
  expect(screen.queryByRole('link', {name: 'Download clip'})).not.toBeInTheDocument();

  const pollCount = pending.filter(request => request.url === '/render-jobs/job-expired').length;
  await advancePolling(10000);
  expect(pending.filter(request => request.url === '/render-jobs/job-expired')).toHaveLength(pollCount);

  fireEvent.click(screen.getByRole('button', {name: 'Render again'}));
  expect(pending.filter(request => request.url === '/render-jobs')).toHaveLength(2);
});

test('render-job polling aborts and timers are cleaned up on unmount', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  const view = render(<App/>);
  await flush();
  await selectSession('Alpha');
  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const createRequest = requestFor(pending, url => url === '/render-jobs');
  await resolveRequestWith(createRequest, {id: 'job-cleanup', status: 'queued'}, {status: 202});
  const poll = requestFor(pending, url => url === '/render-jobs/job-cleanup');
  await resolveRequest(poll, {id: 'job-cleanup', status: 'queued'});

  view.unmount();
  expect(poll.options.signal.aborted).toBe(true);
  const requestCount = pending.filter(request => request.url === '/render-jobs/job-cleanup').length;
  await advancePolling(10000);
  expect(pending.filter(request => request.url === '/render-jobs/job-cleanup')).toHaveLength(requestCount);
});

test('active render jobs constrain session changes and keep polling alive', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const createRequest = requestFor(pending, url => url === '/render-jobs');
  await resolveRequestWith(createRequest, {id: 'job-session', status: 'queued'}, {status: 202});
  const poll = requestFor(pending, url => url === '/render-jobs/job-session');
  await resolveRequest(poll, {id: 'job-session', status: 'running'});

  const changeButtons = screen.getAllByRole('button', {name: 'Change session'});
  expect(changeButtons).toHaveLength(2);
  changeButtons.forEach(button => {
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute(
      'title', 'Finish or cancel the active render before changing sessions'
    );
  });
  expect(poll.options.signal.aborted).toBe(false);
  expect(screen.getByText('Rendering')).toBeInTheDocument();

  await advancePolling(2000);
  expect(pending.filter(request => request.url === '/render-jobs/job-session')).toHaveLength(2);
});

test('subtitle markup renders safely while search and aria text use plain text', async () => {
  const unsafeMarkup = '<b>Hello</b> <i>world</i> <img src=x onerror=alert(1)>safe';
  const {container} = render(<div>{renderSubtitleMarkup(unsafeMarkup)}</div>);
  expect(container.querySelector('img')).not.toBeInTheDocument();
  expect(container.querySelector('b')).toHaveTextContent('Hello');
  expect(container.textContent).toContain('<img src=x onerror=alert(1)>safe');
  const markup = '<b>Hello</b> <i>world</i> <script>alert(1)</script>safe';
  expect(stripSubtitleMarkup(markup)).toBe('Hello world alert(1)safe');

  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());
  const subtitleRequest = requestFor(pending, url => url.includes('/subtitles/A?subtitle=0'));
  await resolveRequest(subtitleRequest, [{start: 1000, end: 2000, text: markup}]);

  const search = screen.getByRole('textbox', {name: 'Search subtitles'});
  fireEvent.change(search, {target: {value: 'hello world alert(1)safe'}});
  const row = screen.getByRole('button', {name: 'Subtitle at 00:00:01: Hello world alert(1)safe'});
  expect(row).toBeInTheDocument();
  expect(row).not.toHaveAttribute('aria-label', expect.stringContaining('<img'));
  expect(row.querySelector('img')).not.toBeInTheDocument();
});

test('Start and End inputs preserve drafts, commit valid values, and restore rejected values', () => {
  function RangeHarness() {
    const [start, setStart] = React.useState(0);
    const [end, setEnd] = React.useState(60000);
    return (
      <TrimScrubber
        duration={120000}
        startPosition={start}
        endPosition={end}
        onRangeChange={(s, e) => { setStart(s); setEnd(e); }}
        onStartChange={value => { if (value < end - 500) setStart(value); }}
        onEndChange={value => { if (value > start + 500) setEnd(value); }}
      />
    );
  }
  render(<RangeHarness/>);

  const start = screen.getByRole('textbox', {name: 'Start time as hours minutes seconds milliseconds'});
  fireEvent.focus(start);
  fireEvent.change(start, {target: {value: '00:'}});
  expect(start).toHaveValue('00:');
  expect(screen.queryByText(/NaN/)).not.toBeInTheDocument();

  fireEvent.change(start, {target: {value: '00:00:05.500'}});
  fireEvent.keyDown(start, {key: 'Enter'});
  expect(screen.getByRole('slider', {name: 'Clip start time'})).toHaveValue('5500');
  expect(start).toHaveValue('00:00:05.500');

  const end = screen.getByRole('textbox', {name: 'End time as hours minutes seconds milliseconds'});
  fireEvent.focus(end);
  fireEvent.change(end, {target: {value: '00:01:05.250'}});
  fireEvent.blur(end);
  expect(screen.getByRole('slider', {name: 'Clip end time'})).toHaveValue('65250');

  // Start cannot cross End's 500ms minimum gap, so the draft restores canonically.
  fireEvent.focus(start);
  fireEvent.change(start, {target: {value: '00:02:00'}});
  fireEvent.blur(start);
  expect(screen.getByRole('slider', {name: 'Clip start time'})).toHaveValue('5500');
  expect(start).toHaveValue('00:00:05.500');
  expect(screen.getByText(/too close to other bound/)).toBeInTheDocument();

  // Invalid syntax also restores canonical text and feedback.
  fireEvent.focus(start);
  fireEvent.change(start, {target: {value: 'not-a-time'}});
  fireEvent.blur(start);
  expect(start).toHaveValue('00:00:05.500');
  expect(screen.getByText(/Invalid time or too close to other bound/)).toBeInTheDocument();
});

test('session context shows the active session title in the workspace', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');

  expect(screen.getByText('Now clipping')).toBeInTheDocument();
  expect(screen.getByText('Alpha')).toBeInTheDocument();
});
