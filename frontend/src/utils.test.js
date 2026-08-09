import {millisToDuration} from './utils';

test.each([
  [5, '00:00:00.005'],
  [50, '00:00:00.050'],
  [500, '00:00:00.500'],
])('formats %ims with three-digit millisecond precision', (millis, expected) => {
  expect(millisToDuration(millis)).toBe(expected);
});

test('formats whole-second durations without milliseconds', () => {
  expect(millisToDuration(3723000)).toBe('01:02:03');
});
