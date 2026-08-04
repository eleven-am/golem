import { HtmlShell } from './shell';

const DOCUMENT = '<html><head><title>Original</title></head><body>App</body></html>';

describe('HtmlShell', () => {
  it('returns the original document when hosting without metadata defaults', () => {
    expect(new HtmlShell(DOCUMENT).render(undefined, '/deep')).toBe(DOCUMENT);
  });

  it('injects shorthand Open Graph and Twitter metadata', () => {
    const rendered = new HtmlShell(DOCUMENT, {
      title: 'Readable',
      description: 'Read later',
      image: '/cover.png',
    }).render(undefined, '/article/one');

    expect(rendered).toContain('<title>Readable</title>');
    expect(rendered).not.toContain('<title>Original</title>');
    expect(rendered).toContain('<meta name="description" content="Read later">');
    expect(rendered).toContain('<meta property="og:url" content="/article/one">');
    expect(rendered).toContain('<meta property="og:image" content="/cover.png">');
    expect(rendered).toContain('<meta name="twitter:card" content="summary_large_image">');
  });

  it('escapes hostile title, description, values, and custom meta names', () => {
    const rendered = new HtmlShell(DOCUMENT, {
      title: '</title><script>alert(1)</script>',
      description: '" onmouseover="alert(1)',
      meta: { 'og:x" content="broken': '<img src=x onerror=alert(1)>' },
    }).render(undefined, '/');

    expect(rendered).not.toContain('<script>alert(1)</script>');
    expect(rendered).not.toContain('onmouseover="alert(1)"');
    expect(rendered).not.toContain('<img src=x');
    expect(rendered).toContain('&lt;/title&gt;&lt;script&gt;alert(1)&lt;/script&gt;');
    expect(rendered).toContain('&quot; onmouseover=&quot;alert(1)');
    expect(rendered).toContain('og:x&quot; content=&quot;broken');
  });

  it('merges custom meta over shorthand values and deletes null entries', () => {
    const rendered = new HtmlShell(DOCUMENT, {
      title: 'Default',
      description: 'Default description',
      meta: { 'article:section': 'News' },
    }).render({
      title: 'Route',
      meta: {
        'og:title': 'Manual title',
        'og:description': null,
        'article:section': null,
        'twitter:card': 'summary',
      },
    }, '/route');

    expect(rendered).toContain('<title>Route</title>');
    expect(rendered).toContain('<meta property="og:title" content="Manual title">');
    expect(rendered).not.toContain('property="og:description"');
    expect(rendered).not.toContain('property="article:section"');
    expect(rendered).toContain('<meta name="twitter:card" content="summary">');
  });

  it('expands shorthand URLs only when baseUrl is configured', () => {
    const relative = new HtmlShell(DOCUMENT, { image: '/image.png' })
      .render(undefined, '/page');
    const verbatim = new HtmlShell(DOCUMENT, {
      image: 'https://cdn.example/image.png',
      url: 'https://app.example/page',
    }).render(undefined, '/ignored');
    const absolute = new HtmlShell(
      DOCUMENT,
      { image: '/image.png' },
      'https://example.com/base/',
    ).render(undefined, '/page');

    expect(relative).toContain('content="/image.png"');
    expect(relative).toContain('content="/page"');
    expect(verbatim).toContain('content="https://cdn.example/image.png"');
    expect(verbatim).toContain('content="https://app.example/page"');
    expect(absolute).toContain('content="https://example.com/image.png"');
    expect(absolute).toContain('content="https://example.com/page"');
  });

  it('rejects a shell without a closing head', () => {
    expect(() => new HtmlShell('<html><body>Missing head</body></html>')).toThrow(
      'Frontend index is missing a closing </head> tag',
    );
  });
});
