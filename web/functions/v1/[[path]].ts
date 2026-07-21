import { proxyTo, type Env } from '../_proxy';

// Catch-all Pages Function for `/v1/*` — covers provision, deprovision, users,
// and users/:id. Proxies to the provisioning-api backend.
export const onRequest: PagesFunction<Env> = async (context) => {
  return proxyTo({
    request: context.request,
    upstreamBase: context.env.PROVISIONING_API_URL,
    env: context.env,
  });
};
