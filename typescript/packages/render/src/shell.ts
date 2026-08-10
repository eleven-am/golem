import escapeHtml from 'escape-html';
import type { MetaTags } from './types';

function expandUrl(value: string | undefined, baseUrl: string | undefined): string | undefined {
  if (!value || !baseUrl) {
    return value;
  }
  try {
    return new URL(value, baseUrl).toString();
  } catch {
    return value;
  }
}

function metaAttribute(name: string): 'name' | 'property' {
  return name.startsWith('og:') || name.startsWith('article:') ? 'property' : 'name';
}

export class HtmlShell {
  private readonly originalPrefix: string;
  private readonly titlelessPrefix: string;
  private readonly suffix: string;

  constructor(
    html: string,
    private readonly defaults?: MetaTags,
    private readonly baseUrl?: string,
  ) {
    const headClose = html.search(/<\/head\s*>/i);
    if (headClose < 0) {
      throw new Error('Frontend index is missing a closing </head> tag');
    }
    this.originalPrefix = html.slice(0, headClose);
    this.titlelessPrefix = this.originalPrefix.replace(/<title\b[^>]*>[\s\S]*?<\/title\s*>/i, '');
    this.suffix = html.slice(headClose);
  }

  render(metadata: MetaTags | null | undefined, requestUrl: string): string {
    if (metadata == null && this.defaults === undefined) {
      return `${this.originalPrefix}${this.suffix}`;
    }

    const resolved: MetaTags = {
      ...this.defaults,
      ...(metadata ?? {}),
      meta: {
        ...(this.defaults?.meta ?? {}),
        ...(metadata?.meta ?? {}),
      },
    };
    resolved.url = expandUrl(resolved.url ?? requestUrl, this.baseUrl);
    resolved.image = expandUrl(resolved.image, this.baseUrl);

    const values = new Map<string, string | null>();
    values.set('description', resolved.description ?? null);
    values.set('og:type', 'website');
    values.set('og:url', resolved.url ?? null);
    values.set('og:title', resolved.title ?? null);
    values.set('og:description', resolved.description ?? null);
    values.set('og:image', resolved.image ?? null);
    values.set('twitter:card', 'summary_large_image');
    values.set('twitter:title', resolved.title ?? null);
    values.set('twitter:description', resolved.description ?? null);
    values.set('twitter:image', resolved.image ?? null);
    for (const [name, value] of Object.entries(resolved.meta ?? {})) {
      values.set(name, value);
    }

    const tags: string[] = [];
    if (resolved.title !== undefined) {
      tags.push(`<title>${escapeHtml(resolved.title)}</title>`);
    }
    for (const [name, value] of values) {
      if (value === null) {
        continue;
      }
      const attribute = metaAttribute(name);
      tags.push(
        `<meta ${attribute}="${escapeHtml(name)}" content="${escapeHtml(value)}">`,
      );
    }

    const prefix = resolved.title === undefined ? this.originalPrefix : this.titlelessPrefix;
    return `${prefix}\n${tags.join('\n')}\n${this.suffix}`;
  }
}
