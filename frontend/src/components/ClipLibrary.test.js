import React from 'react';
import {act, fireEvent, render, screen, waitFor} from '@testing-library/react';
import ClipLibrary from './ClipLibrary';
import ClipDetail from './ClipDetail';
import {creatorLabel, mediaContext, mediaLabel, millisToHMS} from './clips';

// Deferred-promise helpers so tests can drive fetch responses deterministically.
function deferred() {
  let resolve, reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return {promise, resolve, reject};
}

function jsonResponse(data, overrides = {}) {
  return {
    ok: true, status: 200, statusText: 'OK',
    json: () => Promise.resolve(data),
    ...overrides,
  };
}

function installFetch(handler) {
  const requests = [];
  global.fetch = jest.fn((url, options = {}) => {
    const entry = deferred();
    requests.push({url, options, ...entry});
    const handled = handler ? handler(url, entry, options) : null;
    if (handled) return handled;
    return entry.promise;
  });
  return requests;
}

function byUrl(requests, predicate, occurrence = 0) {
  const matches = requests.filter(r => predicate(r.url));
  return matches[occurrence];
}

async function flush() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function resolveReq(req, data, overrides = {}) {
  await act(async () => {
    req.resolve(jsonResponse(data, overrides));
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

// The backend list response is {clips: [...], isAdmin: bool}. Each clip
// includes canDelete, shareUrl, and publicDownloadUrl. shareUrl and
// publicDownloadUrl are the same stable public inline MP4 URL.
const listResponse = (clips, isAdmin = false) => ({clips, isAdmin});

const sampleClip = (overrides = {}) => ({
  id: 'clip-1',
  title: 'Alpha scene',
  ratingKey: 'rk-1',
  mediaId: 101,
  fromMs: 5000,
  toMs: 65000,
  createdAt: '2026-08-01T12:00:00.000Z',
  shareUrl: 'https://clips.example.test/shared/clips/abc123token/download',
  downloadUrl: '/clips/clip-1/download',
  publicDownloadUrl: 'https://clips.example.test/shared/clips/abc123token/download',
  canDelete: true,
  mediaKind: 'movie',
  movieTitle: 'Alpha',
  movieYear: 2024,
  creatorDisplayName: 'plexuser',
  artworkUrl: '/clips/clip-1/artwork',
  ...overrides,
});

// jsdom does not implement clipboard / execCommand. Each test that needs
// clipboard behaviour installs its own stub so jest.restoreAllMocks() cannot
// reset it mid-suite.
function installCopyStub() {
  document.execCommand = jest.fn(() => true);
  return document.execCommand;
}

afterEach(() => {
  jest.restoreAllMocks();
});

// ---------------------------------------------------------------------------
// Helpers / unit-level
// ---------------------------------------------------------------------------

describe('clip helpers', () => {
  test('millisToHMS formats hours, minutes, seconds', () => {
    expect(millisToHMS(0)).toBe('00:00:00');
    expect(millisToHMS(1500)).toBe('00:00:01');
    expect(millisToHMS(60000)).toBe('00:01:00');
    expect(millisToHMS(3661000)).toBe('01:01:01');
  });

  test('creatorLabel uses creatorDisplayName, falling back to a neutral "Unknown creator" (never UUID/email)', () => {
    expect(creatorLabel({creatorDisplayName: 'plexuser'})).toBe('plexuser');
    expect(creatorLabel({creatorDisplayName: '  spaced  '})).toBe('spaced');
    // Old clips without creatorDisplayName get a neutral label — never UUID-derived.
    expect(creatorLabel({ownerUuid: 'abcdef12-3456'})).toBe('Unknown creator');
    expect(creatorLabel({})).toBe('Unknown creator');
    expect(creatorLabel(null)).toBe('Unknown creator');
  });

  test('mediaLabel renders movie title and year', () => {
    const label = mediaLabel({mediaKind: 'movie', movieTitle: 'Inception', movieYear: 2010});
    expect(label.primary).toBe('Inception');
    expect(label.secondary).toBe('(2010)');
  });

  test('mediaLabel renders show title + SxxExx + episode title', () => {
    const label = mediaLabel({
      mediaKind: 'episode', showTitle: 'The Show',
      seasonNumber: 2, episodeNumber: 7, episodeTitle: 'Pilot',
    });
    expect(label.primary).toBe('The Show');
    expect(label.secondary).toBe('S02E07 Pilot');
  });

  test('mediaLabel degrades gracefully with missing fields', () => {
    expect(mediaLabel({mediaKind: 'movie', movieTitle: 'No Year'})).toEqual({primary: 'No Year', secondary: ''});
    expect(mediaLabel({mediaKind: 'episode', showTitle: 'Show Only'})).toEqual({primary: 'Show Only', secondary: ''});
    // No media metadata → empty label (clip.title is shown separately as the headline).
    expect(mediaLabel({title: 'Fallback'})).toEqual({primary: '', secondary: ''});
    expect(mediaLabel(null)).toEqual({primary: '', secondary: ''});
  });

  test('mediaContext joins primary and secondary with a middle dot', () => {
    expect(mediaContext({mediaKind: 'movie', movieTitle: 'Inception', movieYear: 2010})).toBe('Inception · (2010)');
    expect(mediaContext({mediaKind: 'episode', showTitle: 'The Show', seasonNumber: 2, episodeNumber: 7, episodeTitle: 'Pilot'})).toBe('The Show · S02E07 Pilot');
    expect(mediaContext({title: 'No media'})).toBe('');
    expect(mediaContext(null)).toBe('');
  });
});

// ---------------------------------------------------------------------------
// ClipLibrary list states
// ---------------------------------------------------------------------------

describe('ClipLibrary list states', () => {
  test('loading shows a live busy region, then clips render with title, duration, and created date', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);

    // Loading affordance is announced to assistive tech.
    expect(screen.getByRole('status')).toHaveAttribute('aria-busy', 'true');
    expect(screen.getByText('Loading clips…')).toBeInTheDocument();

    const req = byUrl(requests, u => u === '/clips');
    await resolveReq(req, listResponse([sampleClip()]));

    expect(screen.queryByText('Loading clips…')).not.toBeInTheDocument();
    expect(screen.getByText('Alpha scene')).toBeInTheDocument();
    // 60000ms duration → 00:01:00.
    expect(screen.getByText('00:01:00')).toBeInTheDocument();
    // The open affordance is an accessible button with a descriptive label.
    const openBtn = screen.getByRole('button', {name: /Open clip Alpha scene/});
    expect(openBtn).toBeInTheDocument();
  });

  test('a non-admin list (isAdmin=false) reads as "Your clips" with no creator chip', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'),
      listResponse([
        sampleClip({id: 'a', ownerUuid: 'user-own-uuid'}),
        sampleClip({id: 'b', title: 'Beta scene', ownerUuid: 'user-own-uuid'}),
      ], false));

    expect(screen.getByText('Your clips')).toBeInTheDocument();
    expect(screen.queryByText('All clips')).not.toBeInTheDocument();
    // No creator chip is rendered for personal scope.
    expect(screen.queryByText(/^by /)).not.toBeInTheDocument();
  });

  test('an admin list (isAdmin=true) reads as "All clips" and differentiates creators by display name', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'),
      listResponse([
        sampleClip({id: 'a', ownerUuid: 'aaaaaaaa-1111', creatorDisplayName: 'alice', title: 'Alpha scene'}),
        sampleClip({id: 'b', ownerUuid: 'bbbbbbbb-2222', creatorDisplayName: 'bob', title: 'Beta scene'}),
      ], true));

    expect(screen.getByText('All clips')).toBeInTheDocument();
    // Two distinct creator chips differentiate the rows by display name.
    expect(screen.getByText('by alice')).toBeInTheDocument();
    expect(screen.getByText('by bob')).toBeInTheDocument();
    expect(screen.getAllByText(/^by /)).toHaveLength(2);
  });

  test('an admin list with old clips (no creatorDisplayName) shows a neutral "Unknown creator" chip, never UUID/email', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'),
      listResponse([
        sampleClip({id: 'a', creatorDisplayName: 'alice', title: 'Alpha scene'}),
        // Old clip: backend no longer serializes ownerUuid and creatorDisplayName
        // is absent. The row must fall back to "Unknown creator", not a UUID.
        sampleClip({id: 'b', title: 'Old clip', creatorDisplayName: '', ownerUuid: ''}),
      ], true));

    expect(screen.getByText('by alice')).toBeInTheDocument();
    expect(screen.getByText('by Unknown creator')).toBeInTheDocument();
    // No UUID-derived text or email appears anywhere.
    expect(document.body.textContent).not.toMatch(/creator [0-9a-f]{8}/);
    expect(document.body.textContent).not.toMatch(/@/);
  });

  test('an admin with an empty library still reads as "All clips" (explicit isAdmin, not ownerUuid inference)', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'), listResponse([], true));

    // The heading reflects the explicit isAdmin flag, not inferred from clips.
    expect(screen.getByText('All clips')).toBeInTheDocument();
    expect(screen.queryByText('Your clips')).not.toBeInTheDocument();
  });

  test('empty state is announced and explains how clips get saved', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'), listResponse([]));

    expect(screen.getByText('No saved clips yet.')).toBeInTheDocument();
    expect(screen.getByText(/Render a clip from an active session/)).toBeInTheDocument();
  });

  test('network error shows an alert and a retry that re-fetches the list', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    const req = byUrl(requests, u => u === '/clips');
    await act(async () => {
      req.resolve({ok: false, status: 503, statusText: 'Service Unavailable', json: () => Promise.resolve({})});
      await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    });

    expect(screen.getByRole('alert')).toHaveTextContent(/Couldn’t load the clip library/);
    expect(screen.getByRole('button', {name: 'Try again'})).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', {name: 'Try again'}));
    await flush();
    const retryReq = byUrl(requests, u => u === '/clips', 1);
    expect(retryReq).toBeDefined();
    await resolveReq(retryReq, listResponse([sampleClip()]));
    expect(screen.getByText('Alpha scene')).toBeInTheDocument();
  });

  test('auth failure surfaces a sign-in message as an alert', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    const req = byUrl(requests, u => u === '/clips');
    await act(async () => {
      req.resolve({ok: false, status: 401, statusText: 'Unauthorized', type: 'basic', json: () => Promise.resolve({})});
      await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    });

    // The sign-in prompt is announced as an alert (variant=error in StateMessage).
    expect(screen.getByRole('alert')).toHaveTextContent('Sign in to view your clips.');
    // No separate "Couldn't load" network error is shown.
    expect(screen.queryByText(/Couldn’t load the clip library/)).not.toBeInTheDocument();
  });

  test('clicking the open area opens the clip detail via onOpenClip', async () => {
    const requests = installFetch();
    const onOpenClip = jest.fn();
    render(<ClipLibrary onOpenClip={onOpenClip} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'), listResponse([sampleClip()]));

    fireEvent.click(screen.getByRole('button', {name: /Open clip Alpha scene/}));
    expect(onOpenClip).toHaveBeenCalledWith('clip-1');
  });

  test('keyboard activation and action-button clicks do not bubble to the open handler (no nested interactive elements)', async () => {
    const requests = installFetch();
    const onOpenClip = jest.fn();
    render(<ClipLibrary onOpenClip={onOpenClip} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'), listResponse([sampleClip({canDelete: true})]));

    // The open area is a native <button> — keyboard activation is handled
    // by the browser, not a custom onKeyDown. Clicking it opens the clip.
    const openBtn = screen.getByRole('button', {name: /Open clip Alpha scene/});
    fireEvent.click(openBtn);
    expect(onOpenClip).toHaveBeenCalledWith('clip-1');

    // The delete and copy buttons are siblings (not children) of the open
    // button, so their clicks do not bubble to the open handler.
    onOpenClip.mockClear();
    fireEvent.click(screen.getByRole('button', {name: /Delete clip Alpha scene/}));
    expect(onOpenClip).not.toHaveBeenCalled();
    // Clean up the dialog so it doesn't interfere with other assertions.
    fireEvent.click(screen.getByRole('button', {name: 'Cancel'}));
    await waitFor(() => expect(screen.queryByRole('heading', {name: /Delete/})).not.toBeInTheDocument());
  });

  test('the Back button reflects an active workspace when present', async () => {
    const requests = installFetch();
    const onBack = jest.fn();
    render(<ClipLibrary onOpenClip={() => {}} onBack={onBack} hasActiveWorkspace/>);
    await resolveReq(byUrl(requests, u => u === '/clips'), listResponse([sampleClip()]));

    const back = screen.getByRole('button', {name: 'Back to editing'});
    fireEvent.click(back);
    expect(onBack).toHaveBeenCalled();
  });

  test('canDelete=false hides the delete control and shows a disabled placeholder', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'),
      listResponse([sampleClip({canDelete: false})]));

    // No enabled delete button; a disabled placeholder communicates the lack of permission.
    expect(screen.queryByRole('button', {name: /Delete clip Alpha scene/})).not.toBeInTheDocument();
    expect(screen.getByRole('button', {name: 'Delete unavailable'})).toBeDisabled();
  });

  test('canDelete=true shows the enabled delete control', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'),
      listResponse([sampleClip({canDelete: true})]));

    expect(screen.getByRole('button', {name: /Delete clip Alpha scene/})).toBeInTheDocument();
    expect(screen.queryByRole('button', {name: 'Delete unavailable'})).not.toBeInTheDocument();
  });

  test('renders durable artwork thumbnail from artworkUrl with decorative alt (opener carries the aria label)', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'),
      listResponse([sampleClip({artworkUrl: '/clips/clip-1/artwork'})]));

    // The thumbnail is decorative (alt="") because the enclosing open button
    // has the full aria-label. The image is still present with the right src.
    const img = document.querySelector('img');
    expect(img).not.toBeNull();
    expect(img).toHaveAttribute('src', '/clips/clip-1/artwork');
    expect(img).toHaveAttribute('alt', '');
    // The open button carries the accessible name, not the image.
    expect(screen.getByRole('button', {name: /Open clip Alpha scene/})).toBeInTheDocument();
  });

  test('a missing artworkUrl renders the placeholder film icon, not a broken image', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'),
      listResponse([sampleClip({artworkUrl: ''})]));

    expect(screen.queryByRole('img')).not.toBeInTheDocument();
  });

  test('a missing-artwork placeholder is decorative, not a nested button inside the opener (no validateDOMNesting warning)', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'),
      listResponse([sampleClip({artworkUrl: ''})]));

    // The open area is a single native button. The decorative placeholder
    // must not be a nested <button> — it's an inline <svg> only.
    const openBtn = screen.getByRole('button', {name: /Open clip Alpha scene/});
    expect(openBtn.tagName).toBe('BUTTON');
    expect(openBtn.querySelector('button')).toBeNull();
    // The placeholder svg is present and marked decorative.
    expect(openBtn.querySelector('svg[aria-hidden="true"]')).not.toBeNull();
  });

  test('an artwork image onError falls back to the placeholder', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'),
      listResponse([sampleClip({artworkUrl: '/clips/clip-1/artwork'})]));

    // The decorative image is present initially.
    const img = document.querySelector('img');
    expect(img).not.toBeNull();
    fireEvent.error(img);
    // After the error, the image is replaced by the placeholder svg (no img).
    expect(document.querySelector('img')).toBeNull();
  });

  test('the row shows the media context line (movie title · year) under the clip title', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'),
      listResponse([sampleClip({
        mediaKind: 'movie', movieTitle: 'Inception', movieYear: 2010,
      })]));

    expect(screen.getByText('Alpha scene')).toBeInTheDocument();
    expect(screen.getByText('Inception · (2010)')).toBeInTheDocument();
  });

  test('the row shows episode context (show · SxxExx episodeTitle) under the clip title', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'),
      listResponse([sampleClip({
        mediaKind: 'episode', showTitle: 'The Show',
        seasonNumber: 2, episodeNumber: 7, episodeTitle: 'Pilot',
        movieTitle: '', movieYear: null,
      })]));

    expect(screen.getByText('The Show · S02E07 Pilot')).toBeInTheDocument();
  });

  test('the row shows partial media context (movie title without year) as just the title', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'),
      listResponse([sampleClip({
        mediaKind: 'movie', movieTitle: 'No Year Movie', movieYear: null,
      })]));

    // Primary present, secondary absent → context is just the primary.
    expect(screen.getByText('No Year Movie')).toBeInTheDocument();
    // No dangling separator.
    expect(screen.queryByText(/·$/)).not.toBeInTheDocument();
  });

  test('the row shows partial episode context (show + code, no episode title)', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'),
      listResponse([sampleClip({
        mediaKind: 'episode', showTitle: 'The Show',
        seasonNumber: 3, episodeNumber: 1, episodeTitle: '',
        movieTitle: '', movieYear: null,
      })]));

    expect(screen.getByText('The Show · S03E01')).toBeInTheDocument();
  });

  test('a row with no media metadata shows no context line', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'),
      listResponse([sampleClip({
        mediaKind: '', movieTitle: '', movieYear: null,
        showTitle: '', seasonNumber: null, episodeNumber: null, episodeTitle: '',
      })]));

    expect(screen.getByText('Alpha scene')).toBeInTheDocument();
    // No media context line is rendered (no "·" separator, no stray label).
    expect(screen.queryByText(/·/)).not.toBeInTheDocument();
  });

  test('action controls are rendered as a sibling row, never clipped by the open button', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'),
      listResponse([sampleClip({canDelete: true})]));

    // The copy and delete controls are present and not children of the open
    // button — they live in their own sibling action row below the content.
    expect(screen.getByRole('button', {name: 'Copy share link'})).toBeInTheDocument();
    expect(screen.getByRole('button', {name: /Delete clip Alpha scene/})).toBeInTheDocument();
    // The open button does not contain the action controls.
    const openBtn = screen.getByRole('button', {name: /Open clip Alpha scene/});
    expect(openBtn).not.toContainElement(screen.getByRole('button', {name: 'Copy share link'}));
    expect(openBtn).not.toContainElement(screen.getByRole('button', {name: /Delete clip Alpha scene/}));
  });

  test('admin row with a long creatorDisplayName truncates but remains visible', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'),
      listResponse([sampleClip({
        ownerUuid: 'aaaaaaaa-1111', creatorDisplayName: 'a-very-long-plex-username',
      })], true));

    expect(screen.getByText('by a-very-long-plex-username')).toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// Copy share link
// ---------------------------------------------------------------------------

describe('copy share link', () => {
  test('copies shareUrl (direct public inline MP4) to the clipboard and confirms with an updated accessible label', async () => {
    const execStub = installCopyStub();
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'), listResponse([sampleClip()]));

    const copyBtn = screen.getByRole('button', {name: 'Copy share link'});
    fireEvent.click(copyBtn);

    // The legacy copy path was invoked with the full share URL.
    await waitFor(() => expect(execStub).toHaveBeenCalledWith('copy'));
    // The button announces the copied state for screen readers.
    await waitFor(() => expect(screen.getByRole('button', {name: 'Copy share link copied'})).toBeInTheDocument());
  });

  test('a clip without a share token renders a disabled, unavailable share control', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'),
      listResponse([sampleClip({shareUrl: '', publicDownloadUrl: ''})]));

    expect(screen.getByRole('button', {name: 'Share link unavailable'})).toBeDisabled();
  });
});

// ---------------------------------------------------------------------------
// Delete confirmation / success / error / auth recovery
// ---------------------------------------------------------------------------

describe('ClipLibrary delete flow', () => {
  test('delete requires confirmation and removes the clip on success with a status toast', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'), listResponse([sampleClip()]));

    // No clip is removed before confirmation.
    expect(screen.getByText('Alpha scene')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', {name: /Delete clip Alpha scene/}));

    // Confirmation dialog is labelled for screen readers.
    expect(screen.getByRole('heading', {name: /Delete Alpha scene\?/})).toBeInTheDocument();
    expect(screen.getByText(/permanently removes the clip/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', {name: 'Delete clip'}));
    // DELETE request issued.
    const deleteReq = byUrl(requests, u => u === '/clips/clip-1');
    expect(deleteReq).toBeDefined();
    expect(deleteReq.options.method).toBe('DELETE');

    await act(async () => {
      deleteReq.resolve({ok: true, status: 204, statusText: 'No Content'});
      await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    });

    // Row removed and success announced.
    await waitFor(() => expect(screen.queryByText('Alpha scene')).not.toBeInTheDocument());
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Clip deleted.'));
  });

  test('cancel closes the dialog without deleting or fetching', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'), listResponse([sampleClip()]));

    fireEvent.click(screen.getByRole('button', {name: /Delete clip Alpha scene/}));
    fireEvent.click(screen.getByRole('button', {name: 'Cancel'}));

    // The dialog unmounts after its exit transition.
    await waitFor(() => expect(screen.queryByRole('heading', {name: /Delete/})).not.toBeInTheDocument());
    expect(screen.getByText('Alpha scene')).toBeInTheDocument();
    expect(requests.some(r => r.options && r.options.method === 'DELETE')).toBe(false);
  });

  test('a 503 delete error keeps the dialog open with a retryable alert', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'), listResponse([sampleClip()]));

    fireEvent.click(screen.getByRole('button', {name: /Delete clip Alpha scene/}));
    fireEvent.click(screen.getByRole('button', {name: 'Delete clip'}));
    const deleteReq = byUrl(requests, u => u === '/clips/clip-1');
    await act(async () => {
      deleteReq.resolve({ok: false, status: 503, statusText: 'Service Unavailable', json: () => Promise.resolve({})});
      await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    });

    // Dialog remains open with an alert; the clip row is still present.
    expect(screen.getByRole('heading', {name: /Delete Alpha scene\?/})).toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent(/Could not delete this clip/);
    expect(screen.getByText('Alpha scene')).toBeInTheDocument();

    // Retry: confirm again issues a fresh DELETE.
    fireEvent.click(screen.getByRole('button', {name: 'Delete clip'}));
    const retryDelete = byUrl(requests, u => u === '/clips/clip-1', 1);
    expect(retryDelete).toBeDefined();
    await act(async () => {
      retryDelete.resolve({ok: true, status: 204, statusText: 'No Content'});
      await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    });
    expect(screen.queryByText('Alpha scene')).not.toBeInTheDocument();
  });

  test('a 404 during delete reconciles the list as already removed', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'), listResponse([sampleClip()]));

    fireEvent.click(screen.getByRole('button', {name: /Delete clip Alpha scene/}));
    fireEvent.click(screen.getByRole('button', {name: 'Delete clip'}));
    const deleteReq = byUrl(requests, u => u === '/clips/clip-1');
    await act(async () => {
      deleteReq.resolve({ok: false, status: 404, statusText: 'Not Found', json: () => Promise.resolve({})});
      await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    });

    // Treated as success — clip removed, dialog closed, no alert.
    await waitFor(() => expect(screen.queryByText('Alpha scene')).not.toBeInTheDocument());
    await waitFor(() => expect(screen.queryByRole('heading', {name: /Delete/})).not.toBeInTheDocument());
  });

  test('a 401 delete auth failure shows a sign-in/reload action instead of a futile retry', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'), listResponse([sampleClip()]));

    fireEvent.click(screen.getByRole('button', {name: /Delete clip Alpha scene/}));
    fireEvent.click(screen.getByRole('button', {name: 'Delete clip'}));
    const deleteReq = byUrl(requests, u => u === '/clips/clip-1');
    await act(async () => {
      deleteReq.resolve({ok: false, status: 401, statusText: 'Unauthorized', type: 'basic', json: () => Promise.resolve({})});
      await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    });

    // The dialog stays open, but the retry button is replaced with a reload
    // action — retrying with an expired session would be futile.
    expect(screen.getByRole('alert')).toHaveTextContent(/session has expired/i);
    expect(screen.getByRole('link', {name: 'Reload and sign in'})).toHaveAttribute('href', '/authUrl');
    // The "Delete clip" retry button is gone.
    expect(screen.queryByRole('button', {name: 'Delete clip'})).not.toBeInTheDocument();
    // The clip row is still present — the delete did not succeed.
    expect(screen.getByText('Alpha scene')).toBeInTheDocument();
  });

  test('delete is disabled while a delete is in flight', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips'), listResponse([sampleClip()]));

    fireEvent.click(screen.getByRole('button', {name: /Delete clip Alpha scene/}));
    fireEvent.click(screen.getByRole('button', {name: 'Delete clip'}));
    await flush();

    // While the DELETE is pending, the confirm button shows in-progress state.
    expect(screen.getByRole('button', {name: 'Deleting…'})).toBeDisabled();
    expect(screen.getByRole('button', {name: 'Cancel'})).toBeDisabled();
  });
});

// ---------------------------------------------------------------------------
// ClipDetail
// ---------------------------------------------------------------------------

describe('ClipDetail', () => {
  test('renders a browser playback video using the public inline MP4 URL, a download link, and the share URL', async () => {
    const requests = installFetch();
    render(<ClipDetail clipId="clip-1" onBack={() => {}} onDeleted={() => {}}/>);
    const req = byUrl(requests, u => u === '/clips/clip-1');
    await resolveReq(req, sampleClip());

    // Browser playback uses the inline public download URL (shareUrl === publicDownloadUrl).
    const video = document.querySelector('video');
    expect(video).not.toBeNull();
    expect(video.getAttribute('src')).toBe('https://clips.example.test/shared/clips/abc123token/download');
    expect(video).toHaveAttribute('controls');
    expect(video).toHaveAccessibleName(/Video player for/);

    // Download affordance uses the authenticated attachment endpoint.
    expect(screen.getByRole('link', {name: 'Download clip'})).toHaveAttribute('href', '/clips/clip-1/download');

    // The public share URL is visible for verification.
    expect(screen.getByText('https://clips.example.test/shared/clips/abc123token/download')).toBeInTheDocument();
  });

  test('a video onError shows a visible error fallback with download and retry controls', async () => {
    const requests = installFetch();
    render(<ClipDetail clipId="clip-1" onBack={() => {}} onDeleted={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips/clip-1'), sampleClip());

    // Initially the video element is present.
    expect(document.querySelector('video')).not.toBeNull();

    // Simulate a browser playback failure.
    fireEvent.error(document.querySelector('video'));

    // The video is replaced by a visible error fallback with an alert.
    expect(screen.getByRole('alert')).toHaveTextContent(/Couldn’t play this clip in the browser/);
    expect(screen.getByText(/download the file and play it locally/i)).toBeInTheDocument();
    // Download and retry controls are available in the fallback. There are
    // two "Download clip" links (the fallback and the action bar); both are
    // valid, so assert at least one points to the authenticated endpoint.
    const downloadLinks = screen.getAllByRole('link', {name: 'Download clip'});
    expect(downloadLinks.length).toBeGreaterThanOrEqual(1);
    expect(downloadLinks.some(l => l.getAttribute('href') === '/clips/clip-1/download')).toBe(true);
    expect(screen.getByRole('button', {name: 'Try again'})).toBeInTheDocument();

    // Retry restores the video element.
    fireEvent.click(screen.getByRole('button', {name: 'Try again'}));
    expect(document.querySelector('video')).not.toBeNull();
  });

  test('a 404 detail load shows an empty-state message rather than an error alert', async () => {
    const requests = installFetch();
    render(<ClipDetail clipId="clip-gone" onBack={() => {}} onDeleted={() => {}}/>);
    const req = byUrl(requests, u => u === '/clips/clip-gone');
    await act(async () => {
      req.resolve({ok: false, status: 404, statusText: 'Not Found', json: () => Promise.resolve({})});
      await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    });

    expect(screen.getByText('This clip is no longer available.')).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    // No retry is offered for a permanent 404.
    expect(screen.queryByRole('button', {name: 'Try again'})).not.toBeInTheDocument();
  });

  test('detail delete confirms, then calls onDeleted and returns to the library', async () => {
    const requests = installFetch();
    const onDeleted = jest.fn();
    render(<ClipDetail clipId="clip-1" onBack={() => {}} onDeleted={onDeleted}/>);
    await resolveReq(byUrl(requests, u => u === '/clips/clip-1'), sampleClip());

    fireEvent.click(screen.getByRole('button', {name: 'Delete clip Alpha scene'}));
    fireEvent.click(screen.getByRole('button', {name: 'Delete clip'}));
    // Both the detail GET and the DELETE hit /clips/clip-1; pick the DELETE
    // by method so we resolve the right deferred request.
    const deleteReq = requests.find(r => r.url === '/clips/clip-1' && r.options && r.options.method === 'DELETE');
    expect(deleteReq).toBeDefined();
    await act(async () => {
      deleteReq.resolve({ok: true, status: 204, statusText: 'No Content'});
      await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    });

    await waitFor(() => expect(onDeleted).toHaveBeenCalledWith('clip-1'));
  });

  test('detail delete auth failure shows a reload action instead of retry', async () => {
    const requests = installFetch();
    render(<ClipDetail clipId="clip-1" onBack={() => {}} onDeleted={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips/clip-1'), sampleClip());

    fireEvent.click(screen.getByRole('button', {name: 'Delete clip Alpha scene'}));
    fireEvent.click(screen.getByRole('button', {name: 'Delete clip'}));
    const deleteReq = requests.find(r => r.url === '/clips/clip-1' && r.options && r.options.method === 'DELETE');
    await act(async () => {
      deleteReq.resolve({ok: false, status: 401, statusText: 'Unauthorized', type: 'basic', json: () => Promise.resolve({})});
      await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    });

    expect(screen.getByRole('alert')).toHaveTextContent(/session has expired/i);
    expect(screen.getByRole('link', {name: 'Reload and sign in'})).toHaveAttribute('href', '/authUrl');
    expect(screen.queryByRole('button', {name: 'Delete clip'})).not.toBeInTheDocument();
  });

  test('canDelete=false hides the delete button and shows a disabled placeholder', async () => {
    const requests = installFetch();
    render(<ClipDetail clipId="clip-1" onBack={() => {}} onDeleted={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips/clip-1'), sampleClip({canDelete: false}));

    expect(screen.queryByRole('button', {name: /Delete clip Alpha scene/})).not.toBeInTheDocument();
    expect(screen.getByRole('button', {name: 'Delete unavailable'})).toBeDisabled();
  });

  test('admin detail (isAdmin=true) shows the creator chip with display name', async () => {
    const requests = installFetch();
    render(<ClipDetail clipId="clip-1" onBack={() => {}} onDeleted={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips/clip-1'),
      sampleClip({creatorDisplayName: 'alice', isAdmin: true}));
    expect(screen.getByText('by alice')).toBeInTheDocument();
  });

  test('admin detail for an old clip (no creatorDisplayName) shows "Unknown creator", never UUID/email', async () => {
    const requests = installFetch();
    render(<ClipDetail clipId="clip-1" onBack={() => {}} onDeleted={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips/clip-1'),
      sampleClip({creatorDisplayName: '', ownerUuid: '', isAdmin: true}));
    expect(screen.getByText('by Unknown creator')).toBeInTheDocument();
    // No UUID-derived text or email in the document.
    expect(document.body.textContent).not.toMatch(/creator [0-9a-f]{8}/);
    expect(document.body.textContent).not.toMatch(/@/);
  });

  test('non-admin creator detail (isAdmin=false) does not show an admin-style creator chip', async () => {
    const requests = installFetch();
    render(<ClipDetail clipId="clip-1" onBack={() => {}} onDeleted={() => {}}/>);
    // A regular creator viewing their own clip must not see a creator chip —
    // only admins get creator differentiation.
    await resolveReq(byUrl(requests, u => u === '/clips/clip-1'),
      sampleClip({creatorDisplayName: 'alice', isAdmin: false}));
    expect(screen.queryByText(/^by /)).not.toBeInTheDocument();
  });

  test('detail shows the clip title as headline and media context as a subtitle', async () => {
    const requests = installFetch();
    render(<ClipDetail clipId="clip-1" onBack={() => {}} onDeleted={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips/clip-1'),
      sampleClip({mediaKind: 'movie', movieTitle: 'Inception', movieYear: 2010}));

    // The user-given clip title is the h4 headline.
    expect(screen.getByRole('heading', {level: 4, name: 'Alpha scene'})).toBeInTheDocument();
    // The media context appears as a subtitle below.
    expect(screen.getByText('Inception · (2010)')).toBeInTheDocument();
  });

  test('detail shows episode media context (show · SxxExx episodeTitle)', async () => {
    const requests = installFetch();
    render(<ClipDetail clipId="clip-1" onBack={() => {}} onDeleted={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips/clip-1'),
      sampleClip({
        mediaKind: 'episode', showTitle: 'The Show',
        seasonNumber: 2, episodeNumber: 7, episodeTitle: 'Pilot',
        movieTitle: '', movieYear: null,
      }));

    expect(screen.getByText('The Show · S02E07 Pilot')).toBeInTheDocument();
  });

  test('detail shows partial media context (movie title without year)', async () => {
    const requests = installFetch();
    render(<ClipDetail clipId="clip-1" onBack={() => {}} onDeleted={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips/clip-1'),
      sampleClip({mediaKind: 'movie', movieTitle: 'No Year Movie', movieYear: null}));

    // Primary present, secondary absent → context is just the primary.
    expect(screen.getByText('No Year Movie')).toBeInTheDocument();
    expect(screen.queryByText(/·/)).not.toBeInTheDocument();
  });

  test('detail shows partial media context (show + code, no episode title)', async () => {
    const requests = installFetch();
    render(<ClipDetail clipId="clip-1" onBack={() => {}} onDeleted={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips/clip-1'),
      sampleClip({
        mediaKind: 'episode', showTitle: 'The Show',
        seasonNumber: 3, episodeNumber: 1, episodeTitle: '',
        movieTitle: '', movieYear: null,
      }));

    expect(screen.getByText('The Show · S03E01')).toBeInTheDocument();
  });

  test('detail video uses artworkUrl as the poster attribute', async () => {
    const requests = installFetch();
    render(<ClipDetail clipId="clip-1" onBack={() => {}} onDeleted={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips/clip-1'),
      sampleClip({artworkUrl: '/clips/clip-1/artwork'}));

    const video = document.querySelector('video');
    expect(video).not.toBeNull();
    expect(video).toHaveAttribute('poster', '/clips/clip-1/artwork');
  });

  test('detail without artwork renders a video with no poster attribute', async () => {
    const requests = installFetch();
    render(<ClipDetail clipId="clip-1" onBack={() => {}} onDeleted={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips/clip-1'),
      sampleClip({artworkUrl: ''}));

    const video = document.querySelector('video');
    expect(video).not.toBeNull();
    expect(video).not.toHaveAttribute('poster');
  });

  test('detail without media metadata shows only the clip title, no media subtitle', async () => {
    const requests = installFetch();
    render(<ClipDetail clipId="clip-1" onBack={() => {}} onDeleted={() => {}}/>);
    await resolveReq(byUrl(requests, u => u === '/clips/clip-1'),
      sampleClip({mediaKind: '', movieTitle: '', movieYear: null, showTitle: ''}));

    expect(screen.getByRole('heading', {level: 4, name: 'Alpha scene'})).toBeInTheDocument();
    // No media context subtitle is rendered.
    expect(screen.queryByText(/·/)).not.toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// Async cleanup
// ---------------------------------------------------------------------------

describe('async cleanup', () => {
  test('ClipLibrary aborts the in-flight list fetch on unmount', async () => {
    const requests = installFetch();
    const view = render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    const req = byUrl(requests, u => u === '/clips');
    expect(req).toBeDefined();
    view.unmount();
    expect(req.options.signal.aborted).toBe(true);
  });

  test('ClipDetail aborts the in-flight detail fetch on unmount', async () => {
    const requests = installFetch();
    const view = render(<ClipDetail clipId="clip-1" onBack={() => {}} onDeleted={() => {}}/>);
    const req = byUrl(requests, u => u === '/clips/clip-1');
    view.unmount();
    expect(req.options.signal.aborted).toBe(true);
  });

  test('re-fetching the list aborts the previous in-flight request', async () => {
    const requests = installFetch();
    render(<ClipLibrary onOpenClip={() => {}} onBack={() => {}}/>);
    const first = byUrl(requests, u => u === '/clips');
    // Trigger a retry which starts a second request before the first resolves.
    await act(async () => {
      first.resolve({ok: false, status: 503, statusText: 'Service Unavailable', json: () => Promise.resolve({})});
      await Promise.resolve(); await Promise.resolve(); await Promise.resolve();
    });
    fireEvent.click(screen.getByRole('button', {name: 'Try again'}));
    await flush();
    const second = byUrl(requests, u => u === '/clips', 1);
    expect(second).toBeDefined();
    expect(second.options.signal.aborted).toBe(false);
  });
});