import type { HeartbeatProbeResult } from '../api/deliveryClient';
import { strings } from '../strings';

/** Short operator text for a `down_reason` or probe-result reason. Unknown reasons get a generic line. */
export function reasonCopy({ reason, status }: { reason: string; status: number | null }): string {
  switch (reason) {
    case 'http_status':
      return status === null
        ? strings.probeReasonHttpStatusUnknown
        : strings.probeReasonHttpStatus({ status });
    case 'timeout':
      return strings.probeReasonTimeout;
    case 'body_mismatch':
      return strings.probeReasonBodyMismatch;
    case 'blocked':
      return strings.probeReasonBlocked;
    case 'tls':
      return strings.probeReasonTls;
    case 'scheme':
      return strings.probeReasonScheme;
    case 'network':
      return strings.probeReasonNetwork;
    case 'config':
      return strings.probeReasonConfig;
    case 'missed':
      return strings.probeReasonMissed;
    case 'fail':
      return strings.probeReasonFail;
    default:
      return strings.probeReasonUnknown;
  }
}

/** One line for a probe's last result: "OK · HTTP 200" or the failure reason. */
export function probeResultCopy({ result }: { result: HeartbeatProbeResult }): string {
  if (result.reason === null) {
    return result.status === null
      ? strings.probeResultOkNoStatus
      : strings.probeResultOk({ status: result.status });
  }
  return reasonCopy({ reason: result.reason, status: result.status });
}
