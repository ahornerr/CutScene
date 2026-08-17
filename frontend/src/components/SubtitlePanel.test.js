import React from 'react'
import {fireEvent, render, screen} from '@testing-library/react'
import SubtitlePanel from './SubtitlePanel'

const props = {
  selectedSubtitle: 0, onSubtitleChange: jest.fn(), streamsLoading: false, streamsError: null,
  subtitleOffsetMs: 0, onSubtitleOffsetChange: jest.fn(), listMode: 'full', listEntries: [], totalCount: 0,
  anchor: -1, selectionRange: {from: -1, to: -1}, onPick: jest.fn(), search: '', onSearchChange: jest.fn(),
  onClearSearch: jest.fn(), loading: false, error: null, listRef: null,
}

test('lists a clear format badge and prioritizes text formats ahead of PGS', () => {
  render(<SubtitlePanel {...props} streams={[
    {index: 0, language: 'English', displayTitle: 'Bitmap', codec: 'pgs', type: 'pgs'},
    {index: 1, language: 'English', displayTitle: 'Dialogue', codec: 'srt', type: 'text'},
    {index: 2, language: 'English', displayTitle: 'Signs', codec: 'ass', type: 'text'},
  ]}/>)
  fireEvent.mouseDown(screen.getByRole('combobox', {name: 'Subtitle track'}))
  const labels = screen.getAllByRole('option').map(option => option.getAttribute('aria-label'))
  expect(labels).toEqual([null, 'English (Dialogue)', 'English (Signs)', 'English (Bitmap)'])
  expect(screen.getByRole('option', {name: 'English (Dialogue)'})).toHaveAttribute('aria-describedby', 'subtitle-format-1')
})
