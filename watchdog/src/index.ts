// notifs-watchdog: a cron-only Worker that probes delivery-api, provisioning-api and ntfy from
// outside the VPS and reports changes (plus a daily summary) to a dedicated Discord webhook.
// It has no fetch handler and no routes, and `workers_dev = false` keeps it off the public
// internet: its only inputs are the cron trigger, its KV state key and its secrets.
import { runWatchdog } from './watchdog';
import type { Env } from './types';

export default {
  async scheduled(controller, env): Promise<void> {
    await runWatchdog({ env, now: controller.scheduledTime });
  },
} satisfies ExportedHandler<Env>;
