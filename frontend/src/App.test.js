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

function installTrackedSessionsFetch() {
  const sessionRequests = [];
  const pending = [];
  global.fetch = jest.fn((url, options = {}) => {
    const request = deferred();
    const tracked = {url, options, ...request};
    if (url === '/sessions') sessionRequests.push(tracked);
    else pending.push(tracked);
    return request.promise;
  });
  return {sessionRequests, pending};
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

async function setDocumentVisibility(state) {
  Object.defineProperty(document, 'visibilityState', {configurable: true, value: state});
  await act(async () => {
    document.dispatchEvent(new Event('visibilitychange'));
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
  const buttons = screen.getAllByRole('button', {name: 'Change source'});
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
  window.history.replaceState(null, '', '#/');
  Object.defineProperty(document, 'visibilityState', {configurable: true, value: 'visible'});
  mockPlayerUrls.length = 0;
});

beforeAll(() => {
  Element.prototype.scrollIntoView = jest.fn();
});

test('subtitle stream discovery sends the selected media part ID URL-encoded', async () => {
  const mediaId = '101';
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

test('sessions owned by the current user are highlighted with a Your session badge', async () => {
  const ownedSessions = [
    {
      ratingKey: 'A', type: 'movie', title: 'Alpha', year: 2024, viewOffset: 0, duration: 120000,
      User: {title: 'viewer'}, Media: [{Part: [{id: '101'}]}],
      ownedByCurrentUser: true,
    },
    {
      ratingKey: 'B', type: 'movie', title: 'Beta', year: 2024, viewOffset: 5000, duration: 120000,
      User: {title: 'someone else'}, Media: [{Part: [{id: '202'}]}],
      ownedByCurrentUser: false,
    },
    {
      ratingKey: 'C', type: 'movie', title: 'Gamma', year: 2024, viewOffset: 0, duration: 120000,
      User: {title: 'viewer'}, Media: [{Part: [{id: '303'}]}],
      // ownedByCurrentUser omitted → treated as not owned (graceful default).
    },
  ];
  installFetch(response(ownedSessions));
  render(<App/>);
  await flush();

  // The owned card carries the visible "Your session" badge and a data hook.
  const alphaCard = screen.getByRole('button', {name: /Your session.*Alpha/});
  expect(alphaCard).toHaveTextContent('Your session');
  expect(alphaCard).toHaveAttribute('data-owned', 'true');

  // Explicitly-not-owned card: no badge, no data hook.
  const betaCard = screen.getByRole('button', {name: /Beta/});
  expect(betaCard).not.toHaveTextContent('Your session');
  expect(betaCard).not.toHaveAttribute('data-owned');

  // Field omitted entirely: treated as not owned — no badge.
  const gammaCard = screen.getByRole('button', {name: /Gamma/});
  expect(gammaCard).not.toHaveTextContent('Your session');
  expect(gammaCard).not.toHaveAttribute('data-owned');

  // Exactly one badge across the picker.
  expect(screen.getAllByText('Your session')).toHaveLength(1);
});

test('session cards surface existing metadata: progress, player state, resolution, audio, location', async () => {
  const richSessions = [
    {
      ratingKey: 'A', type: 'episode',
      grandparentTitle: 'The Show', parentIndex: 2, index: 7, title: 'A Long Descriptive Episode Title',
      year: 2024, viewOffset: 1500000, duration: 3000000,
      thumb: '/thumb/A', User: {title: 'viewer'}, Media: [{Part: [{id: '101'}], videoResolution: '1080', audioChannels: 6}],
      Player: {title: 'Plex Web (Chrome)', state: 'playing'},
      Session: {location: 'lan'},
      ownedByCurrentUser: true,
    },
    {
      ratingKey: 'B', type: 'movie', title: 'Beta', year: 2023, viewOffset: 0, duration: 5400000,
      thumb: '/thumb/B', User: {title: 'someone else'}, Media: [{Part: [{id: '202'}], videoResolution: '4k', audioChannels: 8}],
      Player: {title: 'Apple TV', state: 'paused'},
      Session: {location: 'wan'},
      ownedByCurrentUser: false,
    },
    {
      ratingKey: 'C', type: 'movie', title: 'Gamma', year: 2022, viewOffset: 0, duration: 3600000,
      thumb: '/thumb/C', User: {title: 'viewer'}, Media: [{Part: [{id: '303'}]}],
      // No Player / Session.location, minimal Media — quality + footer gracefully absent.
    },
  ];
  installFetch(response(richSessions));
  render(<App/>);
  await flush();

  // Alpha — episode primary is the show title; secondary is S02E07 + episode title.
  const alphaCard = screen.getByRole('button', {name: /Your session.*The Show/});
  expect(alphaCard).toHaveTextContent('The Show');
  expect(alphaCard).toHaveTextContent('S02E07 A Long Descriptive Episode Title');
  // Progress (viewOffset 1,500,000ms / duration 3,000,000ms → 50%).
  expect(alphaCard).toHaveTextContent('00:25:00');
  expect(alphaCard).toHaveTextContent('00:50:00');
  // Player state dot label.
  expect(alphaCard).toHaveTextContent('Playing');
  // Resolution and audio are independently scannable chips.
  expect(alphaCard).toHaveTextContent('1080');
  expect(alphaCard).toHaveTextContent('5.1');
  // Footer — device + location. The user ('viewer') is omitted on owned cards
  // because the "Your session" badge already conveys ownership.
  expect(alphaCard).toHaveTextContent('Plex Web (Chrome)');
  expect(alphaCard).toHaveTextContent('Local');
  expect(alphaCard).not.toHaveTextContent('viewer');

  // Beta — paused, 4k/7.1, remote, not owned.
  const betaCard = screen.getByRole('button', {name: /Beta/});
  expect(betaCard).toHaveTextContent('Paused');
  expect(betaCard).toHaveTextContent('4K');
  expect(betaCard).toHaveTextContent('7.1');
  // Footer includes the user (not owned), device, and location.
  expect(betaCard).toHaveTextContent('someone else');
  expect(betaCard).toHaveTextContent('Apple TV');
  expect(betaCard).toHaveTextContent('Remote');
  // Not owned → no badge.
  expect(betaCard).not.toHaveTextContent('Your session');

  // Gamma — minimal metadata: no player-state label, no quality pill, no footer.
  const gammaCard = screen.getByRole('button', {name: /Gamma/});
  expect(gammaCard).not.toHaveTextContent('Playing');
  expect(gammaCard).not.toHaveTextContent('Paused');
  expect(gammaCard).not.toHaveTextContent('Local');
  expect(gammaCard).not.toHaveTextContent('Remote');
  expect(gammaCard).not.toHaveTextContent('Your session');
  expect(gammaCard).not.toHaveTextContent('·');
  // Duration still surfaces as the upper bound.
  expect(gammaCard).toHaveTextContent('01:00:00');
});

test('episode cards fall back to the show poster when the episode has no thumbnail', async () => {
  const noThumbSessions = [
    {
      ratingKey: 'A', type: 'episode',
      grandparentTitle: 'The Show', parentIndex: 1, index: 1, title: 'Pilot',
      viewOffset: 0, duration: 1200000,
      // No thumb; grandparentThumb should be used instead.
      grandparentThumb: '/show/poster',
      User: {title: 'viewer'}, Media: [{Part: [{id: '101'}]}],
      ownedByCurrentUser: true,
    },
    {
      ratingKey: 'B', type: 'movie', title: 'NoArt Movie', year: 2024, viewOffset: 0, duration: 1200000,
      // Neither thumb nor grandparentThumb → placeholder, no broken image request.
      User: {title: 'viewer'}, Media: [{Part: [{id: '202'}]}],
    },
  ];
  installFetch(response(noThumbSessions));
  render(<App/>);
  await flush();

  // Episode card uses the show poster URL.
  const episodeCard = screen.getByRole('button', {name: /Your session.*The Show/});
  const episodeImg = episodeCard.querySelector('img');
  expect(episodeImg).not.toBeNull();
  expect(episodeImg.getAttribute('src')).toBe('/thumb?path=/show/poster');

  // Movie with no artwork renders a placeholder and no <img> at all.
  const movieCard = screen.getByRole('button', {name: /NoArt Movie/});
  expect(movieCard.querySelector('img')).toBeNull();
  // The placeholder SVG is present.
  expect(movieCard.querySelector('svg')).not.toBeNull();
});

test('owned sessions sort first while preserving original relative order within each group', async () => {
  // Mix owned and non-owned in a scrambled order; each group keeps its
  // original relative order after the stable partition.
  const mixedSessions = [
    {ratingKey: 'N1', type: 'movie', title: 'Non-A', year: 2024, viewOffset: 0, duration: 60000,
      User: {title: 'x'}, Media: [{Part: [{id: '1'}]}], ownedByCurrentUser: false},
    {ratingKey: 'O1', type: 'movie', title: 'Own-A', year: 2024, viewOffset: 0, duration: 60000,
      User: {title: 'me'}, Media: [{Part: [{id: '2'}]}], ownedByCurrentUser: true},
    {ratingKey: 'N2', type: 'movie', title: 'Non-B', year: 2024, viewOffset: 0, duration: 60000,
      User: {title: 'y'}, Media: [{Part: [{id: '3'}]}], ownedByCurrentUser: false},
    {ratingKey: 'O2', type: 'movie', title: 'Own-B', year: 2024, viewOffset: 0, duration: 60000,
      User: {title: 'me'}, Media: [{Part: [{id: '4'}]}], ownedByCurrentUser: true},
    {ratingKey: 'N3', type: 'movie', title: 'Non-C', year: 2024, viewOffset: 0, duration: 60000,
      User: {title: 'z'}, Media: [{Part: [{id: '5'}]}], ownedByCurrentUser: false},
  ];
  installFetch(response(mixedSessions));
  render(<App/>);
  await flush();

  // The cards render in DOM order. Owned cards (O1, O2) come first, preserving
  // their original relative order; non-owned (N1, N2, N3) follow, also in
  // their original relative order.
  const cards = screen.getAllByRole('button');
  const cardTitles = cards.map(card => card.textContent);
  const indexOf = title => cardTitles.findIndex(t => t.includes(title));
  const o1 = indexOf('Own-A');
  const o2 = indexOf('Own-B');
  const n1 = indexOf('Non-A');
  const n2 = indexOf('Non-B');
  const n3 = indexOf('Non-C');

  // All found.
  [o1, o2, n1, n2, n3].forEach(i => expect(i).toBeGreaterThanOrEqual(0));

  // Owned group leads, in original relative order.
  expect(o1).toBeLessThan(o2);
  expect(o2).toBeLessThan(n1);

  // Non-owned group preserves original relative order.
  expect(n1).toBeLessThan(n2);
  expect(n2).toBeLessThan(n3);
});

test('sessions error state offers a retry that re-fetches the sessions list', async () => {
  const failing = jest.fn(() => Promise.reject(new Error('boom')));
  global.fetch = jest.fn((url) => {
    if (url === '/sessions') return failing();
    return Promise.resolve(response([]));
  });
  render(<App/>);
  await flush();

  expect(screen.getByText(/Couldn’t load your Plex sessions/)).toBeInTheDocument();
  const retryButton = screen.getByRole('button', {name: 'Try again'});
  expect(retryButton).toBeInTheDocument();

  // First fetch failed; retry re-fetches.
  expect(failing).toHaveBeenCalledTimes(1);
  fireEvent.click(retryButton);
  await flush();
  expect(failing).toHaveBeenCalledTimes(2);
});

test('refreshes sessions on a ten-second picker cadence and restarts immediately on return', async () => {
  jest.useFakeTimers();
  const {sessionRequests} = installTrackedSessionsFetch();
  render(<App/>);

  expect(sessionRequests).toHaveLength(1);
  await resolveRequest(sessionRequests[0], sessions);
  await advancePolling(9999);
  expect(sessionRequests).toHaveLength(1);
  await advancePolling(1);
  expect(sessionRequests).toHaveLength(2);

  await resolveRequest(sessionRequests[1], sessions);
  await advancePolling(10000);
  expect(sessionRequests).toHaveLength(3);
  // A refresh already in flight is aborted before opening the workspace.
  await selectSession('Alpha');
  expect(sessionRequests[2].options.signal.aborted).toBe(true);

  await changeSession();
  expect(sessionRequests).toHaveLength(4);
});

test('visibility pauses and aborts refreshes, then performs an immediate refresh when visible', async () => {
  jest.useFakeTimers();
  const {sessionRequests} = installTrackedSessionsFetch();
  render(<App/>);
  await resolveRequest(sessionRequests[0], sessions);

  await advancePolling(10000);
  expect(sessionRequests).toHaveLength(2);
  await setDocumentVisibility('hidden');
  expect(sessionRequests[1].options.signal.aborted).toBe(true);
  await advancePolling(20000);
  expect(sessionRequests).toHaveLength(2);

  await setDocumentVisibility('visible');
  expect(sessionRequests).toHaveLength(3);
});

test('transient refresh failures retain the last good sessions and retry on the next tick', async () => {
  jest.useFakeTimers();
  const {sessionRequests} = installTrackedSessionsFetch();
  render(<App/>);
  await resolveRequest(sessionRequests[0], sessions);

  await advancePolling(10000);
  await rejectRequest(sessionRequests[1]);
  expect(screen.getByText('Alpha')).toBeInTheDocument();
  expect(screen.queryByText(/Couldn’t load your Plex sessions/)).not.toBeInTheDocument();

  await advancePolling(10000);
  expect(sessionRequests).toHaveLength(3);
});

test('auth responses stop session refresh and show the login state', async () => {
  jest.useFakeTimers();
  const {sessionRequests} = installTrackedSessionsFetch();
  render(<App/>);
  await resolveRequest(sessionRequests[0], sessions);
  await advancePolling(10000);
  await resolveRequestWith(sessionRequests[1], null, {ok: false, status: 401, statusText: 'Unauthorized'});

  expect(screen.getByRole('link', {name: 'Log in with Plex'})).toBeInTheDocument();
  await advancePolling(30000);
  expect(sessionRequests).toHaveLength(2);
});

test('stale refresh results cannot replace sessions after selection', async () => {
  jest.useFakeTimers();
  const {sessionRequests} = installTrackedSessionsFetch();
  render(<App/>);
  await resolveRequest(sessionRequests[0], sessions);
  await advancePolling(10000);
  const stale = [{...sessions[0], title: 'Stale session'}];

  await selectSession('Alpha');
  await resolveRequest(sessionRequests[1], stale);
  await changeSession();

  expect(screen.getByText('Alpha')).toBeInTheDocument();
  expect(screen.queryByText('Stale session')).not.toBeInTheDocument();
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

test('invalid active media IDs stay on the canonical picker without starting workspace work', async () => {
  const invalidSessions = sessions.map(session => session.ratingKey === 'A'
    ? {...session, Media: [{Part: [{id: '101.5'}]}]}
    : session
  );
  const pending = installFetch(response(invalidSessions));
  render(<App/>);
  await flush();
  await selectSession('Alpha');

  expect(window.location.hash).toBe('#/');
  expect(screen.getByText('Pick something to clip')).toBeInTheDocument();
  expect(screen.queryByText('Now clipping')).not.toBeInTheDocument();
  expect(pending.some(request => request.url.startsWith('/streams/'))).toBe(false);
  expect(pending.some(request => request.url === '/render-jobs')).toBe(false);
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

  const changeButtons = screen.getAllByRole('button', {name: 'Change source'});
  expect(changeButtons).toHaveLength(1);
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

// ---------------------------------------------------------------------------
// Theater mode — desktop-only preview toggle (mobile stays single-column).
// ---------------------------------------------------------------------------

async function openWorkspaceWithStreams() {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  await resolveRequest(requestFor(pending, url => url === '/streams/A?mediaId=101'), textStreams());
  return pending;
}

test('theater toggle is rendered near the preview with an accessible pressed state', async () => {
  await openWorkspaceWithStreams();

  const toggle = screen.getByRole('button', {name: 'Theater mode'});
  expect(toggle).toBeInTheDocument();
  // Off by default — aria-pressed communicates state to assistive tech.
  expect(toggle).toHaveAttribute('aria-pressed', 'false');
  expect(toggle).toHaveTextContent('Theater mode');
});

test('clicking the theater toggle flips state, label, and the workspace modifier class', async () => {
  await openWorkspaceWithStreams();

  const toggle = screen.getByRole('button', {name: 'Theater mode'});
  // Workspace starts in the normal two-pane layout.
  expect(document.querySelector('.cs-workspace')).not.toHaveClass('cs-workspace--theater');

  fireEvent.click(toggle);
  await flush();

  // On: pressed, relabeled, and the workspace grid collapses to one column.
  expect(toggle).toHaveAttribute('aria-pressed', 'true');
  expect(toggle).toHaveTextContent('Default view');
  expect(document.querySelector('.cs-workspace')).toHaveClass('cs-workspace--theater');

  // Toggling back restores the normal layout and the original label.
  fireEvent.click(toggle);
  await flush();
  expect(toggle).toHaveAttribute('aria-pressed', 'false');
  expect(toggle).toHaveTextContent('Theater mode');
  expect(document.querySelector('.cs-workspace')).not.toHaveClass('cs-workspace--theater');
});

test('theater mode preserves trim, render, and subtitle controls in the DOM', async () => {
  await openWorkspaceWithStreams();

  fireEvent.click(screen.getByRole('button', {name: 'Theater mode'}));
  await flush();

  // Trim controls remain available.
  expect(screen.getByRole('textbox', {name: 'Start time as hours minutes seconds milliseconds'})).toBeInTheDocument();
  expect(screen.getByRole('textbox', {name: 'End time as hours minutes seconds milliseconds'})).toBeInTheDocument();
  // Render controls remain available.
  expect(screen.getByRole('button', {name: 'Render clip'})).toBeInTheDocument();
  // Subtitle panel remains available beneath the preview.
  expect(screen.getByRole('combobox', {name: 'Subtitle track'})).toBeInTheDocument();
  expect(screen.getByText('Subtitles')).toBeInTheDocument();
});

test('theater mode does not alter the preview URL or trigger a render request', async () => {
  const pending = await openWorkspaceWithStreams();
  const playerUrlBefore = screen.getByTestId('react-player').getAttribute('data-url');
  const renderRequestsBefore = pending.filter(r => r.url === '/render-jobs').length;

  fireEvent.click(screen.getByRole('button', {name: 'Theater mode'}));
  await flush();

  expect(screen.getByTestId('react-player').getAttribute('data-url')).toBe(playerUrlBefore);
  expect(pending.filter(r => r.url === '/render-jobs').length).toBe(renderRequestsBefore);
});

test('changing sessions resets theater mode to its default off state', async () => {
  const pending = await openWorkspaceWithStreams();

  fireEvent.click(screen.getByRole('button', {name: 'Theater mode'}));
  await flush();
  expect(document.querySelector('.cs-workspace')).toHaveClass('cs-workspace--theater');

  await changeSession();
  // Theater toggle is gone while the picker is showing.
  expect(screen.queryByRole('button', {name: 'Theater mode'})).not.toBeInTheDocument();

  await selectSession('Beta');
  await resolveRequest(requestFor(pending, url => url === '/streams/B?mediaId=202'), textStreams());

  // New session opens in the normal two-pane layout with the toggle off.
  expect(document.querySelector('.cs-workspace')).not.toHaveClass('cs-workspace--theater');
  const toggle = screen.getByRole('button', {name: 'Theater mode'});
  expect(toggle).toHaveAttribute('aria-pressed', 'false');
  expect(toggle).toHaveTextContent('Theater mode');
});

test('theater toggle is not rendered before a session is selected', async () => {
  installFetch();
  render(<App/>);
  await flush();

  expect(screen.queryByRole('button', {name: 'Theater mode'})).not.toBeInTheDocument();
});

// ---------------------------------------------------------------------------
// Clip library navigation + saved-clip discoverability
// ---------------------------------------------------------------------------

test('the Clips header button opens the library and Back returns to sessions', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();

  // Header exposes a Clips navigation affordance once authenticated.
  const clipsBtn = screen.getByRole('button', {name: 'Clips'});
  fireEvent.click(clipsBtn);
  await flush();

  // The library fetches /clips; resolve with the authenticated list shape
  // {clips, isAdmin} to confirm the view switched.
  const clipsReq = requestFor(pending, url => url === '/clips');
  await resolveRequest(clipsReq, {clips: [], isAdmin: false});
  expect(screen.getByText('No saved clips yet.')).toBeInTheDocument();
  // The session picker is no longer rendered.
  expect(screen.queryByText('Pick something to clip')).not.toBeInTheDocument();

  // Back returns to the session picker.
  fireEvent.click(screen.getByRole('button', {name: 'Back to sessions'}));
  await flush();
  expect(screen.getByText('Pick something to clip')).toBeInTheDocument();
});

test('navigating to the clip library pauses session polling (no /sessions fetches while away)', async () => {
  jest.useFakeTimers();
  const {sessionRequests, pending} = installTrackedSessionsFetch();
  render(<App/>);
  await resolveRequest(sessionRequests[0], sessions);

  // Navigate to the library view.
  fireEvent.click(screen.getByRole('button', {name: 'Clips'}));
  await flush();
  // Resolve the clips list so the library view settles.
  const clipsReq = requestFor(pending, url => url === '/clips');
  await resolveRequest(clipsReq, {clips: [], isAdmin: false});

  // Advance well past the 10s refresh cadence; no new /sessions fetch fires.
  const sessionsBefore = sessionRequests.length;
  await advancePolling(20000);
  expect(sessionRequests.length).toBe(sessionsBefore);

  // Returning home resumes the refresh cadence immediately.
  fireEvent.click(screen.getByRole('button', {name: 'Back to sessions'}));
  await flush();
  expect(sessionRequests.length).toBe(sessionsBefore + 1);
});

test('selecting an active session serializes its canonical workspace URL', async () => {
  installFetch(response(sessions));
  render(<App/>);
  await flush();

  await selectSession('Alpha');

  expect(window.location.hash).toBe('#/workspace/A?mediaId=101');
  expect(screen.getByText('Now clipping')).toBeInTheDocument();
});

test('active render rejects a different workspace hash and keeps polling the active source', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');

  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const createRequest = requestFor(pending, url => url === '/render-jobs');
  await resolveRequestWith(createRequest, {id: 'job-route-guard', status: 'queued'}, {status: 202});
  const poll = requestFor(pending, url => url === '/render-jobs/job-route-guard');
  await resolveRequest(poll, {id: 'job-route-guard', status: 'running'});

  await act(async () => {
    const changed = new Promise(resolve => window.addEventListener('hashchange', resolve, {once: true}));
    window.location.hash = '#/workspace/B?mediaId=202';
    await changed;
  });
  await flush();

  expect(window.location.hash).toBe('#/workspace/A?mediaId=101');
  expect(screen.getByText('Alpha')).toBeInTheDocument();
  expect(screen.getByText('Rendering')).toBeInTheDocument();
  expect(poll.options.signal.aborted).toBe(false);
  expect(pending.some(request => request.url.startsWith('/streams/B'))).toBe(false);
});

test('workspace selection followed by browser back or home shows the picker at #/', async () => {
  const pending = installFetch(response(sessions));
  render(<App/>);
  await flush();
  await selectSession('Alpha');

  expect(window.location.hash).toBe('#/workspace/A?mediaId=101');
  expect(screen.getByText('Now clipping')).toBeInTheDocument();

  await act(async () => {
    const changed = new Promise(resolve => window.addEventListener('hashchange', resolve, {once: true}));
    window.history.back();
    await changed;
  });
  await flush();

  expect(window.location.hash).toBe('#/');
  expect(screen.getByText('Pick something to clip')).toBeInTheDocument();
  expect(screen.queryByText('Now clipping')).not.toBeInTheDocument();

  await selectSession('Alpha');
  fireEvent.click(screen.getByRole('button', {name: 'CutScene home'}));
  await flush();
  expect(window.location.hash).toBe('#/');
  expect(screen.getByText('Pick something to clip')).toBeInTheDocument();

  fireEvent.click(screen.getByRole('button', {name: 'Clips'}));
  await flush();
  await resolveRequest(requestFor(pending, url => url === '/clips'), {clips: [], isAdmin: false});
  fireEvent.click(screen.getByRole('button', {name: 'Back to editing'}));
  await flush();
  expect(window.location.hash).toBe('#/workspace/A?mediaId=101');
  expect(screen.getByText('Now clipping')).toBeInTheDocument();
});

test('an active workspace deep link hydrates only the matching live media part', async () => {
  window.history.replaceState(null, '', '#/workspace/A?mediaId=101');
  const pending = installFetch(response(sessions));
  render(<App/>);
  await flush();

  expect(screen.getByText('Now clipping')).toBeInTheDocument();
  expect(screen.getByText('Alpha')).toBeInTheDocument();
  expect(window.location.hash).toBe('#/workspace/A?mediaId=101');
  expect(requestFor(pending, url => url === '/streams/A?mediaId=101')).toBeDefined();
});

test('initial clip deep links are rendered from the hash', async () => {
  window.history.replaceState(null, '', '#/clips/deep-link');
  const pending = installFetch();
  render(<App/>);
  await flush();

  const detailReq = requestFor(pending, url => url === '/clips/deep-link');
  await resolveRequest(detailReq, {
    id: 'deep-link', title: 'Deep linked clip', ratingKey: 'A', mediaId: 101,
    fromMs: 0, toMs: 60000, createdAt: '2026-08-01T12:00:00.000Z',
    shareUrl: 'https://clips.example.test/shared/deep-link',
    downloadUrl: '/clips/deep-link/download',
    publicDownloadUrl: 'https://clips.example.test/shared/deep-link',
    canDelete: false, isAdmin: false,
  });

  expect(screen.getByText('Deep linked clip')).toBeInTheDocument();
  expect(window.location.hash).toBe('#/clips/deep-link');
});

test('in-app hash navigation updates history and browser back/forward restores views', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();

  fireEvent.click(screen.getByRole('button', {name: 'Clips'}));
  await flush();
  expect(window.location.hash).toBe('#/clips');
  await resolveRequest(requestFor(pending, url => url === '/clips'), {clips: [], isAdmin: false});
  expect(screen.getByText('No saved clips yet.')).toBeInTheDocument();

  fireEvent.click(screen.getByRole('button', {name: 'Back to sessions'}));
  await flush();
  expect(window.location.hash).toBe('#/');
  expect(screen.getByText('Pick something to clip')).toBeInTheDocument();

  await act(async () => {
    const changed = new Promise(resolve => window.addEventListener('hashchange', resolve, {once: true}));
    window.history.back();
    await changed;
  });
  await flush();
  expect(window.location.hash).toBe('#/clips');
  await resolveRequest(requestFor(pending, url => url === '/clips', 1), {clips: [], isAdmin: false});
  expect(screen.getByText('No saved clips yet.')).toBeInTheDocument();

  await act(async () => {
    const changed = new Promise(resolve => window.addEventListener('hashchange', resolve, {once: true}));
    window.history.forward();
    await changed;
  });
  await flush();
  expect(window.location.hash).toBe('#/');
  expect(screen.getByText('Pick something to clip')).toBeInTheDocument();
});

test('a successful render surfaces the saved clip via Open in library without losing the transient download', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');
  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const createRequest = requestFor(pending, url => url === '/render-jobs');
  await resolveRequestWith(createRequest, {id: 'job-saved', status: 'queued'}, {status: 202});

  let poll = requestFor(pending, url => url === '/render-jobs/job-saved');
  await resolveRequest(poll, {id: 'job-saved', status: 'running'});
  await advancePolling();
  poll = pending.filter(r => r.url === '/render-jobs/job-saved').slice(-1)[0];

  // A successful render is promoted to a durable clip; the terminal response
  // carries clipId alongside the transient downloadUrl.
  const expiresAt = new Date(Date.now() + 1500).toISOString();
  await resolveRequest(poll, {
    id: 'job-saved', status: 'succeeded',
    downloadUrl: '/render-jobs/job-saved/download', expiresAt,
    clipId: 'clip-saved', shareUrl: 'https://clips.example.test/shared/clips/tok/download',
  });

  // The transient download remains the primary quick-grab action.
  expect(screen.getByRole('link', {name: 'Download clip'})).toHaveAttribute('href', '/render-jobs/job-saved/download');
  // The durable saved clip is surfaced distinctly.
  expect(screen.getByText('Saved to your clip library.')).toBeInTheDocument();
  expect(screen.getByText(/Render job status: Ready\. Download ready\. Saved to your clip library\./))
    .toHaveAttribute('aria-live', 'polite');

  // Opening the saved clip navigates to the detail view, which fetches the
  // clip metadata. The workspace state is retained (returning is possible).
  fireEvent.click(screen.getByRole('button', {name: 'Open in library'}));
  await flush();
  const detailReq = requestFor(pending, url => url === '/clips/clip-saved');
  await resolveRequest(detailReq, {
    id: 'clip-saved', title: 'Alpha scene', ratingKey: 'A', mediaId: 101,
    fromMs: 0, toMs: 60000, createdAt: '2026-08-01T12:00:00.000Z',
    shareUrl: 'https://clips.example.test/shared/clips/tok/download',
    downloadUrl: '/clips/clip-saved/download',
    publicDownloadUrl: 'https://clips.example.test/shared/clips/tok/download',
    canDelete: true,
    isAdmin: false,
  });
  expect(screen.getByText('Alpha scene')).toBeInTheDocument();
  // Browser playback uses the inline public download URL (shareUrl === publicDownloadUrl).
  expect(document.querySelector('video').getAttribute('src')).toBe('https://clips.example.test/shared/clips/tok/download');
});

// ---------------------------------------------------------------------------
// Phase 2 — Plex library search source selection
// ---------------------------------------------------------------------------
//
// The library search panel is presented as a peer source choice to active
// sessions. Selecting a library result normalizes it into a session-shaped
// object and routes partId through streams/subtitles/preview/render, while
// active-session sources remain unchanged (no partId).

const libraryResults = [
  {
    ratingKey: 'L1', mediaId: 5001, partId: 6001, type: 'movie',
    title: 'Library Movie', year: 2023, duration: 5400000,
    artwork: '/thumb/L1', videoResolution: '1080', audioChannels: 6,
  },
  {
    ratingKey: 'L2', mediaId: 5002, partId: 6002, type: 'episode',
    title: 'Pilot', grandparentTitle: 'Library Show', seasonNumber: 1, episodeNumber: 1,
    duration: 2700000, artwork: '/thumb/L2', videoResolution: '720', audioChannels: 2,
  },
];

test('a library workspace deep link hydrates through the explicit source endpoint', async () => {
  window.history.replaceState(null, '', '#/workspace/L1?mediaId=5001&partId=6001');
  const pending = installFetch();
  render(<App/>);
  await flush();

  const sourceRequest = requestFor(pending, url => url.startsWith('/library/source/'));
  expect(sourceRequest.url).toBe('/library/source/L1?mediaId=5001&partId=6001');
  await resolveRequest(sourceRequest, libraryResults[0]);

  expect(window.location.hash).toBe('#/workspace/L1?mediaId=5001&partId=6001');
  expect(screen.getByText('From your Plex library')).toBeInTheDocument();
  expect(requestFor(pending, url => url === '/streams/L1?mediaId=5001&partId=6001')).toBeDefined();
});

test('returning home after library workspace hydration restarts session loading', async () => {
  window.history.replaceState(null, '', '#/workspace/L1?mediaId=5001&partId=6001');
  const {sessionRequests, pending} = installTrackedSessionsFetch();
  render(<App/>);
  await flush();

  const sourceRequest = requestFor(pending, url => url === '/library/source/L1?mediaId=5001&partId=6001');
  await resolveRequest(sourceRequest, libraryResults[0]);
  expect(screen.getByText('From your Plex library')).toBeInTheDocument();

  fireEvent.click(screen.getByRole('button', {name: 'CutScene home'}));
  await flush();
  expect(window.location.hash).toBe('#/');
  expect(sessionRequests).toHaveLength(1);

  await resolveRequest(sessionRequests[0], sessions);
  expect(screen.getByText('Pick something to clip')).toBeInTheDocument();
  expect(screen.queryByText('Loading sessions…')).not.toBeInTheDocument();
});

function librarySearchRequest(pending, occurrence = 0) {
  return requestFor(pending, url => url.startsWith('/library/search?query='), occurrence);
}

async function searchLibrary(query) {
  const input = screen.getByRole('textbox', {name: 'Search Plex library'});
  fireEvent.change(input, {target: {value: query}});
  // Debounce is 300ms with real timers; advance microtasks only.
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function searchLibraryWithFakeTimers(query) {
  const input = screen.getByRole('textbox', {name: 'Search Plex library'});
  fireEvent.change(input, {target: {value: query}});
  // Only flush microtasks — the test advances the debounce timer explicitly.
  await act(async () => { await Promise.resolve(); });
}

test('library search panel is presented as a peer source choice on the home view', async () => {
  installFetch(response(sessions));
  render(<App/>);
  await flush();

  // The unified picker has one shared result region: active sessions are the
  // default contents, with library search taking over after a valid query.
  expect(screen.getByRole('textbox', {name: 'Search Plex library'})).toBeInTheDocument();
  expect(screen.getByText('Alpha')).toBeInTheDocument();
  expect(screen.queryByText('Active sessions')).not.toBeInTheDocument();
});

test('library search debounces the query and does not fire below the 2-character minimum', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();

  // A single character is below the minimum — no search request fires.
  await searchLibraryWithFakeTimers('A');
  expect(pending.some(r => r.url.startsWith('/library/search'))).toBe(false);

  // A second character crosses the minimum, but the debounce hasn't elapsed.
  await searchLibraryWithFakeTimers('Al');
  expect(pending.some(r => r.url.startsWith('/library/search'))).toBe(false);

  // After the 300ms debounce, the search request fires.
  await act(async () => { jest.advanceTimersByTime(300); });
  const searchReq = librarySearchRequest(pending);
  expect(searchReq).toBeDefined();
  expect(decodeURIComponent(searchReq.url)).toBe('/library/search?query=Al');
});

test('library search shows loading, then results with title, context, year, duration, and quality', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();

  jest.useFakeTimers();
  await searchLibraryWithFakeTimers('show');
  await act(async () => { jest.advanceTimersByTime(300); });
  jest.useRealTimers();

  // Loading state is announced before results resolve.
  expect(screen.getByText('Searching…')).toBeInTheDocument();

  await resolveRequest(librarySearchRequest(pending), libraryResults);

  // Movie result — title, year, duration, quality pill.
  const movieCard = screen.getByRole('button', {name: /Library result.*Library Movie/});
  expect(movieCard).toHaveTextContent('Library Movie');
  expect(movieCard).toHaveTextContent('(2023)');
  expect(movieCard).toHaveTextContent('1080');
  expect(movieCard).toHaveTextContent('5.1');
  expect(movieCard).toHaveTextContent('Movie');
  expect(movieCard).toHaveTextContent('01:30:00');

  // Episode result — show title, S01E01 + episode title, quality, duration.
  const episodeCard = screen.getByRole('button', {name: /Library result.*Library Show/});
  expect(episodeCard).toHaveTextContent('Library Show');
  expect(episodeCard).toHaveTextContent('S01E01 Pilot');
  expect(episodeCard).toHaveTextContent('720');
  expect(episodeCard).toHaveTextContent('2.0');
  expect(episodeCard).toHaveTextContent('Episode');
  expect(episodeCard).toHaveTextContent('00:45:00');

  // Both cards carry the distinguishing "Library" badge.
  expect(screen.getAllByText('Library')).toHaveLength(2);
});

test('library search empty state is presented clearly', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();

  await searchLibraryWithFakeTimers('zzz');
  await act(async () => { jest.advanceTimersByTime(300); });
  await resolveRequest(librarySearchRequest(pending), []);
  jest.useRealTimers();

  expect(screen.getByText('No matching titles in your Plex library.')).toBeInTheDocument();
  expect(screen.getByText('Try a different search term.')).toBeInTheDocument();
});

test('library search error state offers a retry that re-runs the search', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();

  await searchLibraryWithFakeTimers('fail');
  await act(async () => { jest.advanceTimersByTime(300); });
  const firstSearch = librarySearchRequest(pending);
  await resolveRequestWith(firstSearch, {}, {ok: false, status: 503});
  jest.useRealTimers();

  expect(screen.getByText(/Couldn’t search the Plex library/)).toBeInTheDocument();
  expect(screen.getByRole('button', {name: 'Try again'})).toBeInTheDocument();

  fireEvent.click(screen.getByRole('button', {name: 'Try again'}));
  const secondSearch = librarySearchRequest(pending, 1);
  expect(secondSearch).toBeDefined();
  await resolveRequest(secondSearch, libraryResults);
  expect(screen.getByText('Library Movie')).toBeInTheDocument();
});

test('selecting a library result opens the workspace and passes partId through streams, preview, and render', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();

  await searchLibraryWithFakeTimers('movie');
  await act(async () => { jest.advanceTimersByTime(300); });
  await resolveRequest(librarySearchRequest(pending), [libraryResults[0]]);
  jest.useRealTimers();

  fireEvent.click(screen.getByRole('button', {name: /Library result.*Library Movie/}));
  await flush();

  // The workspace opens with the library source context.
  expect(window.location.hash).toBe('#/workspace/L1?mediaId=5001&partId=6001');
  expect(screen.getByText('Now clipping')).toBeInTheDocument();
  expect(screen.getByText('From your Plex library')).toBeInTheDocument();
  expect(screen.getByText('Library Movie')).toBeInTheDocument();

  // Streams request carries partId for the library source.
  const streamsReq = requestFor(pending, url => url.startsWith('/streams/L1?mediaId=5001'));
  expect(streamsReq.url).toBe('/streams/L1?mediaId=5001&partId=6001');

  await resolveRequest(streamsReq, textStreams());

  // Preview URL carries partId, and the clip starts at 00:00:00 (no viewOffset).
  expect(mockPlayerUrls).toEqual([
    '/preview/L1/00:00:00/00:01:00?mediaId=5001&partId=6001&subtitle=0',
  ]);

  // Render-job POST body carries partId for the library source.
  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const createRequest = requestFor(pending, url => url === '/render-jobs');
  expect(JSON.parse(createRequest.options.body)).toMatchObject({
    ratingKey: 'L1', mediaId: 5001, partId: 6001,
    fromMs: 0, toMs: 60000, subtitleIndex: 0, audioMode: 'standard', subtitleOffsetMs: 0,
  });
});

test('library result subtitle prewarm requests carry partId', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();

  await searchLibraryWithFakeTimers('movie');
  await act(async () => { jest.advanceTimersByTime(300); });
  await resolveRequest(librarySearchRequest(pending), [libraryResults[0]]);
  jest.useRealTimers();

  fireEvent.click(screen.getByRole('button', {name: /Library result.*Library Movie/}));
  const streamsReq = requestFor(pending, url => url.startsWith('/streams/L1?mediaId=5001'));
  await resolveRequest(streamsReq, [
    {index: 0, type: 'text', codec: 'srt', displayTitle: 'Selected'},
    {index: 1, type: 'text', codec: 'webvtt', displayTitle: 'Prewarm'},
  ]);

  // The unselected text track is prewarmed with partId.
  const prewarm = pending.find(r => r.url.includes('/subtitles/L1?subtitle=1'));
  expect(prewarm).toBeDefined();
  expect(prewarm.url).toBe('/subtitles/L1?subtitle=1&mediaId=5001&partId=6001');
});

test('library result subtitle entry fetch carries partId', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();

  await searchLibraryWithFakeTimers('movie');
  await act(async () => { jest.advanceTimersByTime(300); });
  await resolveRequest(librarySearchRequest(pending), [libraryResults[0]]);
  jest.useRealTimers();

  fireEvent.click(screen.getByRole('button', {name: /Library result.*Library Movie/}));
  await resolveRequest(requestFor(pending, url => url.startsWith('/streams/L1?mediaId=5001')), textStreams());

  // Switching subtitle track fires a subtitle-entry fetch with partId.
  await selectSubtitle('Y track');
  const yRequest = pending.filter(r => r.url.includes('/subtitles/L1?subtitle=1')).slice(-1)[0];
  expect(yRequest.url).toBe('/subtitles/L1?subtitle=1&mediaId=5001&partId=6001');
  await resolveRequest(yRequest, [{start: 1000, end: 2000, text: 'Y one'}]);
  expect(screen.getByText('Y one')).toBeInTheDocument();
});

test('retry render from a library source resubmits the immutable spec with partId', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();

  await searchLibraryWithFakeTimers('movie');
  await act(async () => { jest.advanceTimersByTime(300); });
  await resolveRequest(librarySearchRequest(pending), [libraryResults[0]]);
  jest.useRealTimers();

  fireEvent.click(screen.getByRole('button', {name: /Library result.*Library Movie/}));
  await resolveRequest(requestFor(pending, url => url.startsWith('/streams/L1?mediaId=5001')), textStreams());

  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const firstCreate = requestFor(pending, url => url === '/render-jobs');
  const submittedBody = JSON.parse(firstCreate.options.body);
  expect(submittedBody).toEqual({
    ratingKey: 'L1', mediaId: 5001, partId: 6001,
    fromMs: 0, toMs: 60000, subtitleIndex: 0, audioMode: 'standard', subtitleOffsetMs: 0,
  });

  await resolveRequestWith(firstCreate, {id: 'job-lib-retry', status: 'queued'}, {status: 202});
  const poll = requestFor(pending, url => url === '/render-jobs/job-lib-retry');
  await resolveRequest(poll, {
    id: 'job-lib-retry', status: 'failed',
    error: {code: 'source_unavailable', message: 'Source unavailable.', retryable: true},
  });

  fireEvent.click(screen.getByRole('button', {name: 'Try again'}));
  const secondCreate = requestFor(pending, url => url === '/render-jobs', 1);
  expect(JSON.parse(secondCreate.options.body)).toEqual(submittedBody);
  expect(JSON.parse(secondCreate.options.body)).toMatchObject({partId: 6001});
});

test('changing from a library source back to an active session omits partId (active-session regression)', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();

  // Select a library source first.
  await searchLibraryWithFakeTimers('movie');
  await act(async () => { jest.advanceTimersByTime(300); });
  await resolveRequest(librarySearchRequest(pending), [libraryResults[0]]);
  jest.useRealTimers();
  fireEvent.click(screen.getByRole('button', {name: /Library result.*Library Movie/}));
  await resolveRequest(requestFor(pending, url => url.startsWith('/streams/L1?mediaId=5001')), textStreams());

  // Change source back to the picker, then select an active session.
  await changeSession();
  await selectSession('Alpha');

  // Streams + preview for the active session omit partId entirely.
  const streamsReq = requestFor(pending, url => url === '/streams/A?mediaId=101');
  expect(streamsReq.url).toBe('/streams/A?mediaId=101');
  await resolveRequest(streamsReq, textStreams());
  expect(mockPlayerUrls.slice(-1)[0]).toBe('/preview/A/00:00:00/00:01:00?mediaId=101&subtitle=0');

  // The render payload for an active session omits partId.
  fireEvent.click(screen.getByRole('button', {name: 'Render clip'}));
  const createRequest = requestFor(pending, url => url === '/render-jobs');
  expect(JSON.parse(createRequest.options.body)).toEqual({
    ratingKey: 'A', mediaId: 101, fromMs: 0, toMs: 60000,
    subtitleIndex: 0, audioMode: 'standard', subtitleOffsetMs: 0,
  });
  expect(JSON.parse(createRequest.options.body)).not.toHaveProperty('partId');
});

test('session context shows the source kind for an active session (regression)', async () => {
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectSession('Alpha');

  expect(screen.getByText('Now clipping')).toBeInTheDocument();
  expect(screen.getByText('Active Plex session')).toBeInTheDocument();
  expect(screen.getByRole('button', {name: 'Change source'})).toBeInTheDocument();
});

// ---------------------------------------------------------------------------
// Gate 2 regressions — malformed library-source responses and search races.
// ---------------------------------------------------------------------------

async function selectLibraryResultForRegression(pending, result = libraryResults[0]) {
  await searchLibraryWithFakeTimers('movie');
  await act(async () => { jest.advanceTimersByTime(300); });
  await resolveRequest(librarySearchRequest(pending), [result]);
  jest.useRealTimers();
  fireEvent.click(screen.getByRole('button', {name: /Library result/}));
  await flush();
}

test.each([422, 503])(
  'library-source streams %s responses keep the workspace alive and surface an error',
  async status => {
    jest.useFakeTimers();
    const pending = installFetch();
    render(<App/>);
    await flush();
    await selectLibraryResultForRegression(pending);

    const streamsRequest = requestFor(pending, url => url.startsWith('/streams/L1?mediaId=5001'));
    expect(streamsRequest.url).toBe('/streams/L1?mediaId=5001&partId=6001');
    await resolveRequestWith(streamsRequest, {error: {message: `streams failed (${status})`}}, {
      ok: false, status,
    });

    expect(screen.getByText('From your Plex library')).toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent('Couldn’t load subtitle tracks.');
    expect(screen.getByText('Subtitles')).toBeInTheDocument();
  }
);

test.each([422, 503])(
  'library-source subtitle %s responses keep the workspace alive and surface an error',
  async status => {
    jest.useFakeTimers();
    const pending = installFetch();
    render(<App/>);
    await flush();
    await selectLibraryResultForRegression(pending);

    const streamsRequest = requestFor(pending, url => url.startsWith('/streams/L1?mediaId=5001'));
    await resolveRequest(streamsRequest, [{index: 0, type: 'text', codec: 'srt', displayTitle: 'Only track'}]);
    const subtitleRequest = requestFor(pending, url => url === '/subtitles/L1?subtitle=0&mediaId=5001&partId=6001');
    await resolveRequestWith(subtitleRequest, {error: {message: `subtitle failed (${status})`}}, {
      ok: false, status,
    });

    expect(screen.getByText('From your Plex library')).toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent('Couldn’t load subtitles for this track.');
    expect(screen.getByText('Only track')).toBeInTheDocument();
  }
);

test('non-array library stream bodies degrade to an empty state without crashing', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectLibraryResultForRegression(pending);

  const streamsRequest = requestFor(pending, url => url.startsWith('/streams/L1?mediaId=5001'));
  await resolveRequest(streamsRequest, {streams: 'not an array'});
  expect(screen.getByText('From your Plex library')).toBeInTheDocument();
  expect(screen.getByText('No subtitle tracks available for this session.')).toBeInTheDocument();
});

test('non-array library subtitle bodies degrade to an empty state without crashing', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();
  await selectLibraryResultForRegression(pending);

  const streamsRequest = requestFor(pending, url => url.startsWith('/streams/L1?mediaId=5001'));
  await resolveRequest(streamsRequest, [{index: 0, type: 'text', codec: 'srt', displayTitle: 'Only track'}]);
  const subtitleRequest = requestFor(pending, url => url === '/subtitles/L1?subtitle=0&mediaId=5001&partId=6001');
  await resolveRequest(subtitleRequest, {entries: 'not an array'});

  expect(screen.getByText('From your Plex library')).toBeInTheDocument();
  expect(screen.getByText('No subtitle entries.')).toBeInTheDocument();
});

test('raw library query edits immediately invalidate late results from the old query', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();

  await searchLibraryWithFakeTimers('old');
  await act(async () => { jest.advanceTimersByTime(300); });
  const oldRequest = librarySearchRequest(pending);

  await searchLibraryWithFakeTimers('new');
  expect(oldRequest.options.signal.aborted).toBe(true);

  // Resolve while the replacement query is still inside its debounce window.
  await resolveRequest(oldRequest, [libraryResults[0]]);
  expect(screen.queryByText('Library Movie')).not.toBeInTheDocument();

  await act(async () => { jest.advanceTimersByTime(300); });
  const newResult = {...libraryResults[0], ratingKey: 'L-new', title: 'New query result'};
  const newRequest = librarySearchRequest(pending, 1);
  await resolveRequest(newRequest, [newResult]);
  jest.useRealTimers();
  expect(screen.getByText('New query result')).toBeInTheDocument();
  expect(screen.queryByText('Library Movie')).not.toBeInTheDocument();
});

test('malformed library result IDs are rejected before workspace entry or stream requests', async () => {
  const malformedResults = [
    {...libraryResults[0], ratingKey: 'bad-media', title: 'Bad media ID', mediaId: '5001.5'},
    {...libraryResults[0], ratingKey: 'bad-part', title: 'Bad part ID', partId: 'not-an-id'},
  ];
  const pending = installFetch();
  const warning = jest.spyOn(console, 'warn').mockImplementation(() => {});
  render(<App/>);
  await flush();

  jest.useFakeTimers();
  await searchLibraryWithFakeTimers('bad');
  await act(async () => { jest.advanceTimersByTime(300); });
  await resolveRequest(librarySearchRequest(pending), malformedResults);
  jest.useRealTimers();

  fireEvent.click(screen.getByRole('button', {name: /Bad media ID/}));
  await flush();
  expect(screen.queryByText('Now clipping')).not.toBeInTheDocument();
  expect(pending.some(request => request.url.startsWith('/streams/'))).toBe(false);

  fireEvent.click(screen.getByRole('button', {name: /Bad part ID/}));
  await flush();
  expect(screen.queryByText('Now clipping')).not.toBeInTheDocument();
  expect(pending.some(request => request.url.startsWith('/streams/'))).toBe(false);
  expect(warning).toHaveBeenCalledTimes(2);
});

test('library artwork is encoded and partial episode hierarchy omits SnullEnull', async () => {
  const result = {
    ratingKey: 'L-art', mediaId: 7001, partId: 8001, type: 'episode',
    title: 'Pilot', parentTitle: 'The Show', duration: 1200000,
    artwork: '/library/poster?token=a&path=/show poster',
  };
  const pending = installFetch();
  render(<App/>);
  await flush();

  jest.useFakeTimers();
  await searchLibraryWithFakeTimers('art');
  await act(async () => { jest.advanceTimersByTime(300); });
  await resolveRequest(librarySearchRequest(pending), [result]);
  jest.useRealTimers();

  const card = screen.getByRole('button', {name: /Library result.*The Show.*Pilot/});
  expect(card.querySelector('img')).toHaveAttribute(
    'src', `/thumb?path=${encodeURIComponent(result.artwork)}`
  );
  expect(card).toHaveTextContent('The Show');
  expect(card).toHaveTextContent('Pilot');
  expect(card).not.toHaveTextContent('SnullEnull');
});

test('library search exposes one accessible live status region while loading and after empty results', async () => {
  jest.useFakeTimers();
  const pending = installFetch();
  render(<App/>);
  await flush();
  await searchLibraryWithFakeTimers('status');
  await act(async () => { jest.advanceTimersByTime(300); });

  const status = screen.getByLabelText('Plex library search status');
  expect(status).toHaveAttribute('aria-live', 'polite');
  expect(status).toHaveAttribute('aria-busy', 'true');
  expect(screen.getByText('Searching…')).toBeInTheDocument();

  await resolveRequest(librarySearchRequest(pending), []);
  expect(status).toHaveAttribute('aria-live', 'polite');
  expect(status).not.toHaveAttribute('aria-busy', 'true');
  expect(status).toHaveTextContent('No matching titles in your Plex library.');
});

test('a session-poll re-render does not refire a settled library search', async () => {
  // Regression: App passed an inline onAuthRequired to LibrarySearchPanel, and
  // the panel's search effect once listed it in its dependency array. Session
  // polling on the home view calls setSessions ~every 10s, re-rendering App
  // with a fresh callback identity and retriggering the search effect — a
  // visible periodic refetch for a query that hadn't changed. The effect now
  // reads onAuthRequired through a ref, so only committed query / explicit
  // retry refire.
  jest.useFakeTimers();
  const {sessionRequests, pending} = installTrackedSessionsFetch();
  render(<App/>);
  await resolveRequest(sessionRequests[0], sessions);

  await searchLibraryWithFakeTimers('movie');
  await act(async () => { jest.advanceTimersByTime(300); });
  await resolveRequest(librarySearchRequest(pending), [libraryResults[0]]);

  expect(screen.getByText('Library Movie')).toBeInTheDocument();
  const searchesBefore = pending.filter(r => r.url.startsWith('/library/search')).length;
  expect(searchesBefore).toBe(1);

  // Let the picker cadence run. A refresh request may or may not be created
  // before the test's microtask turn; either way, a session update must not
  // refire the settled library search.
  await advancePolling(10000);
  const refreshRequests = sessionRequests.slice(1);
  for (const request of refreshRequests) await resolveRequest(request, sessions);
  await advancePolling(10000);
  const laterRefreshRequests = sessionRequests.slice(1 + refreshRequests.length);
  for (const request of laterRefreshRequests) await resolveRequest(request, sessions);

  const searchesAfter = pending.filter(r => r.url.startsWith('/library/search')).length;
  expect(searchesAfter).toBe(searchesBefore);
  expect(screen.getByText('Library Movie')).toBeInTheDocument();
});

test('library search classifies authentication, validation, and retryable failures', async () => {
  const cases = [
    {query: 'auth', status: 401, expected: 'auth'},
    {query: 'valid', status: 422, expected: 'validation'},
    {query: 'busy', status: 503, expected: 'retryable'},
  ];

  for (const testCase of cases) {
    jest.useFakeTimers();
    const pending = installFetch();
    const view = render(<App/>);
    await flush();
    await searchLibraryWithFakeTimers(testCase.query);
    await act(async () => { jest.advanceTimersByTime(300); });
    const searchRequest = librarySearchRequest(pending);
    const body = testCase.status === 422 ? {error: {message: 'The search query is invalid.'}} : {};
    await resolveRequestWith(searchRequest, body, {ok: false, status: testCase.status});

    if (testCase.expected === 'auth') {
      expect(screen.getByRole('link', {name: 'Log in with Plex'})).toBeInTheDocument();
    } else if (testCase.expected === 'validation') {
      expect(screen.getByRole('alert')).toHaveTextContent('The search query is invalid.');
      expect(screen.queryByRole('button', {name: 'Try again'})).not.toBeInTheDocument();
    } else {
      expect(screen.getByRole('alert')).toHaveTextContent('Couldn’t search the Plex library.');
      expect(screen.getByRole('button', {name: 'Try again'})).toBeInTheDocument();
    }
    view.unmount();
    jest.useRealTimers();
  }
});
