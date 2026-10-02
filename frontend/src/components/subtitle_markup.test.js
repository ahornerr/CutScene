import React from 'react';
import {render} from '@testing-library/react';
import {renderSubtitleMarkup, stripSubtitleMarkup} from './subtitle-markup';

// Subtitle text is untrusted input. These cover both the safety guarantee and
// faithful rendering of literal characters that merely look like markup.
describe('subtitle markup', () => {
  test('keeps a literal < that does not form a tag', () => {
    expect(stripSubtitleMarkup('5 < 10 and 10 > 5')).toBe('5 < 10 and 10 > 5');
  });

  test('keeps a literal < before an unknown attribute-like tag', () => {
    expect(stripSubtitleMarkup('<img src=x onerror=alert(1)>')).toBe('<img src=x onerror=alert(1)>');
  });

  test('does not execute unknown tags', () => {
    const {container} = render(<div>{renderSubtitleMarkup('<img src=x onerror="alert(1)">')}</div>);
    // Unknown tags must not become elements, and must survive as literal text.
    expect(container.querySelector('img')).toBeNull();
    expect(container.textContent).toBe('<img src=x onerror="alert(1)">');
  });

  test('renders allowed formatting', () => {
    const {container} = render(<div>{renderSubtitleMarkup('a <i>b</i> c')}</div>);
    expect(container.querySelector('i')).not.toBeNull();
    expect(container.textContent).toBe('a b c');
  });
});
