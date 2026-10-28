// Pins the timezone for every Vitest worker so fixture time strings are
// deterministic on any host. Runs in the main process before workers fork.
export default function setup(): void {
  process.env.TZ = 'UTC';
}
