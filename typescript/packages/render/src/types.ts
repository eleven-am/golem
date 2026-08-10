export interface MetaTags {
  title?: string;
  description?: string;
  image?: string;
  url?: string;
  meta?: Record<string, string | null>;
}

export interface GolemRenderOptions {
  client: string;
  reserved?: readonly string[];
  index?: string;
  defaults?: MetaTags;
  baseUrl?: string;
}

export const GOLEM_RENDER_OPTIONS = 'GOLEM_RENDER_OPTIONS';
