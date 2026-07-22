import { proxyTo } from './_proxy';
import type { Env } from './_proxy';

// Pages Function for `/people` — proxies to the person-service backend.
export const onRequest: PagesFunction<Env> = async (context) => {
  return proxyTo({
    request: context.request,
    upstreamBase: context.env.PERSON_SERVICE_URL,
    env: context.env,
  });
};
