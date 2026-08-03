export function getApiBaseUrl(): string {
  // LocalHub plugin patch (2026-04-29): respect vite's BASE_URL so that
  // axios calls work at `/app/weknora/` (LocalHub reverse proxy). Without
  // this · axios hits `/api/v1/...` at LocalHub root · gets 404 "Cannot
  // POST". Strip trailing slash so axios doesn't produce `/app/weknora//api/v1/...`.
  // See: plugins/weknora/patches/api-base-baseurl.patch
  // `import.meta.env` is undefined outside Vite (node:test via tsx, plain SSR),
  // so fall back to the origin root there instead of throwing.
  const base = (import.meta.env?.BASE_URL || '/').replace(/\/+$/, '');
  return base;
}

/**
 * 给根相对路径（`/api/v1/...`、`/files` 等）挂上 Vite BASE_URL 前缀。
 *
 * 子路径部署（如 `VITE_BASE_URL=/app/weknora/`）时，浏览器请求的
 * `/api/v1/...` 会落到网关根路径而不是应用前缀，代理返回 404。凡是不经
 * axios（axios 已由 baseURL 统一处理）而直接 `fetch`/`window.open` 的
 * 应用内路径都要经过这里。
 *
 * `base` 参数仅用于测试：默认值取当前构建的 BASE_URL。
 */
export function withApiBasePath(path: string, base: string = getApiBaseUrl()): string {
  const normalized = base.replace(/\/+$/, '');
  if (!normalized || normalized === '/') return path;
  return normalized + path;
}

/**
 * Resolve the API base URL (including the `/api/v1` suffix) into an absolute
 * URL that can be displayed and copy-pasted (curl examples, skill env vars…).
 *
 * VITE BASE_URL may be:
 *   - `''` or `'/'`       -> use the current page origin
 *   - a path prefix like `'/app/weknora/'` -> resolve against the page origin
 *   - an absolute URL like `'https://api.example.com/app'` -> use as-is
 *
 * When no origin is available (SSR, `file://` page where
 * `window.location.origin === 'null'`) it falls back to the relative path.
 */
export function getAbsoluteApiBaseUrl(): string {
  const configured = getApiBaseUrl().trim().replace(/\/+$/, '');
  if (/^https?:\/\//i.test(configured)) {
    return `${configured}/api/v1`;
  }
  const origin =
    typeof window !== 'undefined' && window.location && window.location.origin !== 'null'
      ? window.location.origin
      : '';
  // VITE BASE_URL should already start with `/`, but normalize just in case so
  // we never produce `https://hostapp/weknora/api/v1`.
  const path = configured && !configured.startsWith('/') ? `/${configured}` : configured;
  return `${origin}${path}/api/v1`;
}
