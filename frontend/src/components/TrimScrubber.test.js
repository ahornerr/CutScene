import React from 'react';
import {fireEvent, render, screen} from '@testing-library/react';
import TrimScrubber from './TrimScrubber';

const baseProps = {
  duration: 120000,
  startPosition: 0,
  endPosition: 60000,
  onRangeChange: jest.fn(),
  onStartChange: jest.fn(),
  onEndChange: jest.fn(),
};

beforeEach(() => jest.clearAllMocks());

test('Escape restores a timestamp draft without invoking its callback', () => {
  render(<TrimScrubber {...baseProps}/>);
  const start = screen.getByRole('textbox', {name: 'Start time as hours minutes seconds milliseconds'});

  fireEvent.focus(start);
  fireEvent.change(start, {target: {value: '00:00:12.000'}});
  fireEvent.keyDown(start, {key: 'Escape'});

  expect(start).toHaveValue('00:00:00');
  expect(baseProps.onStartChange).not.toHaveBeenCalled();
});

test('malformed timestamps are rejected without invoking a callback', () => {
  render(<TrimScrubber {...baseProps}/>);
  const end = screen.getByRole('textbox', {name: 'End time as hours minutes seconds milliseconds'});

  fireEvent.focus(end);
  fireEvent.change(end, {target: {value: 'not-a-timestamp'}});
  fireEvent.blur(end);

  expect(end).toHaveValue('00:01:00');
  expect(end).toHaveAttribute('aria-describedby', 'cs-end-time-helper');
  expect(baseProps.onEndChange).not.toHaveBeenCalled();
  expect(screen.getAllByText(/Enter applies · Esc restores/)[0]).toBeInTheDocument();
});

test('exactly the minimum 500ms range is accepted for typed timestamps', () => {
  render(<TrimScrubber {...baseProps}/>);
  const start = screen.getByRole('textbox', {name: 'Start time as hours minutes seconds milliseconds'});

  fireEvent.focus(start);
  fireEvent.change(start, {target: {value: '00:00:59.500'}});
  fireEvent.blur(start);

  expect(baseProps.onStartChange).toHaveBeenCalledWith(59500);
});

test('nudge controls disable at the exact minimum boundary', () => {
  render(<TrimScrubber {...baseProps} startPosition={59500} endPosition={60000}/>);

  expect(screen.getByRole('button', {name: 'Move start later by 1 second'})).toBeDisabled();
  expect(screen.getByRole('button', {name: 'Move end earlier by 1 second'})).toBeDisabled();
  fireEvent.click(screen.getByRole('button', {name: 'Move start earlier by 1 second'}));
  expect(baseProps.onStartChange).toHaveBeenCalledWith(58500);
});

test('focus-window changes keep the focused slider valid after local movement', () => {
  render(<TrimScrubber {...baseProps} startPosition={50000} endPosition={51000}/>);
  const initialFocusedStart = screen.getByRole('slider', {name: 'Focused trim timeline, clip start'});
  const initialMin = initialFocusedStart.getAttribute('aria-valuemin');
  const initialMax = initialFocusedStart.getAttribute('aria-valuemax');
  fireEvent.click(screen.getByRole('button', {name: '10s'}));

  const focusedStart = screen.getByRole('slider', {name: 'Focused trim timeline, clip start'});
  const focusedEnd = screen.getByRole('slider', {name: 'Focused trim timeline, clip end'});
  expect(focusedStart.getAttribute('aria-valuemin')).not.toBe(initialMin);
  expect(focusedStart.getAttribute('aria-valuemax')).not.toBe(initialMax);
  expect(Number(focusedStart.getAttribute('aria-valuemax'))).toBeGreaterThanOrEqual(Number(focusedStart.value));
  expect(Number(focusedEnd.getAttribute('aria-valuemin'))).toBeLessThanOrEqual(Number(focusedEnd.value));
});

test('slider movement commits once after Arrow/Shift-Arrow changes', () => {
  render(<TrimScrubber {...baseProps}/>);
  const focusedStart = screen.getByRole('slider', {name: 'Focused trim timeline, clip start'});

  fireEvent.keyDown(focusedStart, {key: 'ArrowRight'});
  fireEvent.keyDown(focusedStart, {key: 'ArrowRight', shiftKey: true});

  expect(baseProps.onRangeChange).toHaveBeenCalledTimes(1);
  baseProps.onRangeChange.mock.calls.forEach(([start, end]) => {
    expect(end - start).toBeGreaterThanOrEqual(500);
  });
});

test('step presets expose numeric accessible labels', () => {
  render(<TrimScrubber {...baseProps}/>);

  expect(screen.getByRole('button', {name: 'Fine step 100 milliseconds'})).toBeInTheDocument();
  expect(screen.getByRole('button', {name: 'Normal step 1000 milliseconds'})).toBeInTheDocument();
  expect(screen.getByRole('button', {name: 'Coarse step 5000 milliseconds'})).toBeInTheDocument();
});
