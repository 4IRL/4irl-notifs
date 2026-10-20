import { useState } from 'react';
import type { JSX } from 'react';

import type { DeliveryApiClient } from '../api/deliveryClient';
import { DestinationsSection } from './DestinationsSection';
import { RoutesSection } from './RoutesSection';
import type { DestinationsLoadState } from './RoutesSection';

interface DeliveryRegistryProps {
  client: DeliveryApiClient;
  /** The destinations list as last reported; owned by App so Heartbeats shares the one fetch. */
  destinationsState: DestinationsLoadState;
  /** Receives every Destinations load-state report (initial load, add, edit, disable, delete). */
  onDestinationsStateChange: (state: DestinationsLoadState) => void;
}

/**
 * Destinations and Routes of the Delivery tab. Destinations own their fetch and
 * report it through `onDestinationsStateChange`; Routes read the reported list
 * (for the form's checkboxes and the Add hint) and refetch after a destination
 * delete, since delete cascades to routes.
 */
export function DeliveryRegistry({
  client,
  destinationsState,
  onDestinationsStateChange,
}: DeliveryRegistryProps): JSX.Element {
  const [routesRefresh, setRoutesRefresh] = useState(0);

  return (
    <>
      <DestinationsSection
        client={client}
        onLoadStateChange={onDestinationsStateChange}
        onDestinationDeleted={() => setRoutesRefresh((tick) => tick + 1)}
      />
      <RoutesSection
        client={client}
        destinationsState={destinationsState}
        refreshSignal={routesRefresh}
      />
    </>
  );
}
