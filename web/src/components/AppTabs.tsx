import { useEffect, useRef, useState } from 'react';
import type { KeyboardEvent, ReactNode } from 'react';

import { strings } from '../strings';
import './AppTabs.css';

type TabId = 'provisioning' | 'delivery';

const TAB_ORDER: readonly TabId[] = ['provisioning', 'delivery'];

const TAB_LABELS: Record<TabId, string> = {
  provisioning: strings.tabProvisioning,
  delivery: strings.tabDelivery,
};

interface AppTabsProps {
  provisioning: ReactNode;
  delivery: ReactNode;
}

function tabFromHash(hash: string): TabId {
  return hash === '#delivery' ? 'delivery' : 'provisioning';
}

/**
 * Provisioning / Delivery tab shell. Renders the tablist and the `<main>`
 * landmark; each panel is a child of `<main>`. Both panels stay mounted and the
 * inactive one is `hidden`, so state in either survives a tab switch. The
 * Delivery content mounts on the first activation only, keeping its fetches
 * lazy. The selected tab mirrors `location.hash`; a click replaces the history
 * entry (no Back/Forward per tab) and `hashchange` follows external changes.
 */
export function AppTabs({ provisioning, delivery }: AppTabsProps) {
  const [selected, setSelected] = useState<TabId>(() => tabFromHash(window.location.hash));
  const [deliveryVisited, setDeliveryVisited] = useState(selected === 'delivery');
  const tabRefs = useRef<Record<TabId, HTMLButtonElement | null>>({
    provisioning: null,
    delivery: null,
  });

  useEffect(() => {
    function handleHashChange() {
      const next = tabFromHash(window.location.hash);
      setSelected(next);
      if (next === 'delivery') {
        setDeliveryVisited(true);
      }
    }
    window.addEventListener('hashchange', handleHashChange);
    return () => {
      window.removeEventListener('hashchange', handleHashChange);
    };
  }, []);

  function activate(tab: TabId, { focus }: { focus: boolean }) {
    setSelected(tab);
    if (tab === 'delivery') {
      setDeliveryVisited(true);
    }
    const { pathname, search } = window.location;
    window.history.replaceState(null, '', `${pathname}${search}#${tab}`);
    if (focus) {
      tabRefs.current[tab]?.focus();
    }
  }

  function handleKeyDown({ event, tab }: { event: KeyboardEvent<HTMLButtonElement>; tab: TabId }) {
    const index = TAB_ORDER.indexOf(tab);
    let nextIndex: number;
    switch (event.key) {
      case 'ArrowRight':
        nextIndex = (index + 1) % TAB_ORDER.length;
        break;
      case 'ArrowLeft':
        nextIndex = (index - 1 + TAB_ORDER.length) % TAB_ORDER.length;
        break;
      case 'Home':
        nextIndex = 0;
        break;
      case 'End':
        nextIndex = TAB_ORDER.length - 1;
        break;
      default:
        return;
    }
    event.preventDefault();
    activate(TAB_ORDER[nextIndex], { focus: true });
  }

  return (
    <>
      <div role="tablist" aria-label={strings.tabsAriaLabel} className="app-tabs">
        {TAB_ORDER.map((tab) => (
          <button
            key={tab}
            ref={(element) => {
              tabRefs.current[tab] = element;
            }}
            type="button"
            role="tab"
            id={`app-tab-${tab}`}
            aria-selected={selected === tab}
            aria-controls={`app-panel-${tab}`}
            tabIndex={selected === tab ? 0 : -1}
            className="app-tabs__tab"
            onClick={() => {
              activate(tab, { focus: false });
            }}
            onKeyDown={(event) => {
              handleKeyDown({ event, tab });
            }}
          >
            {TAB_LABELS[tab]}
          </button>
        ))}
      </div>
      <main className="app__main">
        <div
          role="tabpanel"
          id="app-panel-provisioning"
          aria-labelledby="app-tab-provisioning"
          hidden={selected !== 'provisioning'}
          className="app-tabs__panel app-tabs__panel--provisioning"
        >
          {provisioning}
        </div>
        <div
          role="tabpanel"
          id="app-panel-delivery"
          aria-labelledby="app-tab-delivery"
          hidden={selected !== 'delivery'}
          className="app-tabs__panel app-tabs__panel--delivery"
        >
          {deliveryVisited ? delivery : null}
        </div>
      </main>
    </>
  );
}
