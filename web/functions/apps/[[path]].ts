import { proxyTo } from '../_proxy';
import type { Env } from '../_proxy';

// Catch-all Pages Function for `/apps` and `/apps/:app_id` — covers the app
// registry list/create/edit/delete. Proxies to the person-service backend
// (which owns the registry alongside the person reverse-index). `proxyTo`
// preserves the request pathname, so `/apps/urls4irl` forwards to
// `${PERSON_SERVICE_URL}/apps/urls4irl`.
export const onRequest: PagesFunction<Env> = async (context) => {
  return proxyTo({
    request: context.request,
    upstreamBase: context.env.PERSON_SERVICE_URL,
    env: context.env,
  });
};
