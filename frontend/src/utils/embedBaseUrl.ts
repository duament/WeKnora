import { getApiBaseUrl } from './api-base'

/**
 * Normalize a configured http(s) base URL to `origin + path`, inheriting the
 * app's build-time sub-path (Vite BASE_URL) when the configured URL has none.
 * Returns null for anything that is not an http(s) URL.
 */
export function normalizeEmbedBaseUrl(raw: string, pageHref?: string): string | null {
  try {
    const u = new URL(raw, pageHref)
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return null
    const pathname = u.pathname.replace(/\/+$/, '')
    return pathname ? `${u.origin}${pathname}` : `${u.origin}${getApiBaseUrl()}`
  } catch {
    return null
  }
}

/**
 * Resolve the public base (origin + optional sub-path) used in embed iframe /
 * widget URLs. `EMBED_BASE_URL` / `VITE_EMBED_BASE_URL` may point at a
 * dedicated embed origin and include a path prefix (e.g.
 * `https://embed.example.com/weknora/`); without an override the current origin
 * plus the app's Vite BASE_URL is used, so sub-path deployments
 * (e.g. `/app/weknora/`) produce working embed URLs.
 */
export function resolveEmbedBaseUrl(): string {
  if (typeof window === 'undefined') return ''
  const runtime = (window as Window & { __RUNTIME_CONFIG__?: { EMBED_BASE_URL?: string } }).__RUNTIME_CONFIG__
  const fromRuntime = runtime?.EMBED_BASE_URL?.trim()
  if (fromRuntime) {
    const resolved = normalizeEmbedBaseUrl(fromRuntime, window.location.href)
    if (resolved) return resolved
  }
  const fromEnv = import.meta.env?.VITE_EMBED_BASE_URL?.trim()
  if (fromEnv) {
    const resolved = normalizeEmbedBaseUrl(fromEnv, window.location.href)
    if (resolved) return resolved
  }
  return `${window.location.origin}${getApiBaseUrl()}`
}
