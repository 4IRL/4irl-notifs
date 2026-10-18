import { useState } from 'react';
import type { JSX } from 'react';

import type { DeliveryApiClient } from '../api/deliveryClient';
import { DestinationsSection } from './DestinationsSection';
import { RoutesSection } from './RoutesSection';
import type { DestinationsLoadState } from './RoutesSection';

interface DeliveryRegistryProps {
  client: DeliveryApiClient;
}

/**
 * Destinations and Routes of the Delivery tab. Destinations own their fetch;
 * Routes read the reported list (for the form's checkboxes and the Add hint)
 * and refetch after a destination delete, since delete cascades to routes.
 */
export function DeliveryRegistry({ client }: DeliveryRegistryProps): JSX.Element {
  const [destinationsState, setDestinationsState] = useState<DestinationsLoadState>({
    status: 'loading',
    destinations: [],
  });
  const [routesRefresh, setRoutesRefresh] = useState(0);

  return (
    <>
      <DestinationsSection
        client={client}
        onLoadStateChange={setDestinationsState}
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
