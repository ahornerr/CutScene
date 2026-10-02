import React from 'react';
import {render, screen} from '@testing-library/react';
import SessionCard from './SessionCard';

function renderWithThumb(thumb) {
  return render(
    <SessionCard
      session={{Key: 's1', title: 'Session One', thumb}}
      active={false}
      onSelect={() => {}}
    />,
  );
}

// Regression test: Plex thumbnail paths can carry a query string. Building the
// /thumb URL without encoding split it at the "&", so the server received a
// truncated path and the rest leaked in as unrelated query parameters.
describe('SessionCard thumbnail URL', () => {
  test('encodes a thumbnail path that contains a query string', () => {
    const thumb = '/library/metadata/1/thumb/1?width=300&h=200';
    const {unmount} = renderWithThumb(thumb);
    const img = screen.getByRole('img');
    const url = new URL(img.getAttribute('src'), 'http://localhost');

    expect(url.searchParams.get('path')).toBe(thumb);
    expect(url.searchParams.get('h')).toBeNull();
    unmount();
  });

  test('encodes a plain thumbnail path unchanged', () => {
    const thumb = '/library/metadata/1/thumb/1';
    const {unmount} = renderWithThumb(thumb);
    const img = screen.getByRole('img');
    const url = new URL(img.getAttribute('src'), 'http://localhost');

    expect(url.searchParams.get('path')).toBe(thumb);
    unmount();
  });

  test('does not break on characters that need escaping', () => {
    const thumb = '/show poster #1/&thumb?x=1';
    const {unmount} = renderWithThumb(thumb);
    const img = screen.getByRole('img');
    const url = new URL(img.getAttribute('src'), 'http://localhost');

    expect(url.searchParams.get('path')).toBe(thumb);
    unmount();
  });

  test('falls back to the grandparent thumbnail', () => {
    const {unmount} = render(
      <SessionCard
        session={{Key: 's1', title: 'Episode', grandparentThumb: '/grandparent/thumb/9'}}
        active={false}
        onSelect={() => {}}
      />,
    );
    const img = screen.getByRole('img');
    const url = new URL(img.getAttribute('src'), 'http://localhost');

    expect(url.searchParams.get('path')).toBe('/grandparent/thumb/9');
    unmount();
  });
});
