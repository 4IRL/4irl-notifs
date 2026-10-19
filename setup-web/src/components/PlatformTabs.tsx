import { useRef } from 'react';
import type { KeyboardEvent, ReactNode } from 'react';

import type { Platform } from '../platform';
import { strings } from '../strings';
import './PlatformTabs.css';

const TAB_ORDER: readonly Platform[] = ['ios', 'android'];

const TAB_LABELS: Record<Platform, string> = {
  ios: strings.tabIphone,
  android: strings.tabAndroid,
};

interface PlatformTabsProps {
  // What the user agent reported; fixed for the page's life. Drives the hint.
  detected: Platform | null;
  // The tab currently shown, or null while nothing has been picked.
  selected: Platform | null;
  onSelect: (platform: Platform) => void;
  // Per-platform step content. Rendered only once a tab is selected.
  iosPanel?: ReactNode;
  androidPanel?: ReactNode;
}

function hintFor({
  detected,
  selected,
}: {
  detected: Platform | null;
  selected: Platform | null;
}): string {
  if (selected === null) {
    return strings.hintPickPhone;
  }
  if (selected === detected) {
    return strings.hintDetected(TAB_LABELS[selected]);
  }
  return strings.hintShowing(TAB_LABELS[selected]);
}

/**
 * iPhone / Android toggle. Same ARIA tablist markup and automatic activation as
 * web/src/components/AppTabs.tsx, but with no hash routing: `selected` is owned by
 * the caller and is never written to the URL. Both tabpanels stay mounted and the
 * inactive one is `hidden`, so state inside either panel (a CopyField's "Copied!")
 * survives a platform switch. Neither panel holds content until a tab is selected.
 */
export function PlatformTabs({
  detected,
  selected,
  onSelect,
  iosPanel,
  androidPanel,
}: PlatformTabsProps) {
  const tabRefs = useRef<Record<Platform, HTMLButtonElement | null>>({
    ios: null,
    android: null,
  });

  function activate({ platform, focus }: { platform: Platform; focus: boolean }) {
    onSelect(platform);
    if (focus) {
      tabRefs.current[platform]?.focus();
    }
  }

  function handleKeyDown({
    event,
    platform,
  }: {
    event: KeyboardEvent<HTMLButtonElement>;
    platform: Platform;
  }) {
    // Leave browser shortcuts such as Alt+Left (Back) alone.
    if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) {
      return;
    }
    const index = TAB_ORDER.indexOf(platform);
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
    activate({ platform: TAB_ORDER[nextIndex], focus: true });
  }

  // Roving tabindex. With nothing selected the first tab is the tab stop so the
  // group stays reachable by keyboard.
  function tabIndexFor(platform: Platform): number {
    if (selected === null) {
      return platform === TAB_ORDER[0] ? 0 : -1;
    }
    return selected === platform ? 0 : -1;
  }

  const panels: Record<Platform, ReactNode> = { ios: iosPanel, android: androidPanel };

  return (
    <>
      <div role="tablist" aria-label={strings.tabsAriaLabel} className="setup-page__tabs">
        {TAB_ORDER.map((platform) => (
          <button
            key={platform}
            ref={(element) => {
              tabRefs.current[platform] = element;
            }}
            type="button"
            role="tab"
            id={`platform-tab-${platform}`}
            aria-selected={selected === platform}
            aria-controls={`platform-panel-${platform}`}
            tabIndex={tabIndexFor(platform)}
            className="setup-page__tab"
            onClick={() => {
              activate({ platform, focus: false });
            }}
            onKeyDown={(event) => {
              handleKeyDown({ event, platform });
            }}
          >
            {TAB_LABELS[platform]}
          </button>
        ))}
      </div>
      <p className="setup-page__detected" role="status" aria-live="polite">
        {hintFor({ detected, selected })}
      </p>
      {TAB_ORDER.map((platform) => (
        <div
          key={platform}
          role="tabpanel"
          id={`platform-panel-${platform}`}
          aria-labelledby={`platform-tab-${platform}`}
          hidden={selected !== platform}
          className="setup-page__panel"
        >
          {selected === null ? null : panels[platform]}
        </div>
      ))}
    </>
  );
}
