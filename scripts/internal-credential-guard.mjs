// Fail-closed guard for the live harnesses that call provisioning-api directly: an empty internal
// credential must stop the suite before any request is sent.
export const EMPTY_CREDENTIAL_MESSAGE =
  'NOTIFS_INTERNAL_CREDENTIAL is empty: set it to the provisioning-api internal ' +
  'credential (make onboard-integration-test reads it from the running container; ' +
  'run make local-up first)';

export function requireInternalCredential(credential) {
  if (!credential) {
    throw new Error(EMPTY_CREDENTIAL_MESSAGE);
  }
  return credential;
}
