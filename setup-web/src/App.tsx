import { useMemo, useState } from 'react';

import { AndroidSteps } from './components/AndroidSteps';
import { InstallCard } from './components/InstallCard';
import { IosSteps } from './components/IosSteps';
import { PlatformTabs } from './components/PlatformTabs';
import { QrHandoff } from './components/QrHandoff';
import { parseSetupParams } from './params';
import { detectPlatform } from './platform';
import type { Platform } from './platform';
import { strings } from './strings';
import './App.css';

interface AppProps {
  search?: string;
  userAgent?: string;
  maxTouchPoints?: number;
}

// Props default to the real window/navigator so production renders `<App />`
// bare while tests inject the inputs instead of patching globals.
export default function App({
  search = window.location.search,
  userAgent = navigator.userAgent,
  maxTouchPoints = navigator.maxTouchPoints,
}: AppProps) {
  const params = useMemo(() => parseSetupParams(search), [search]);
  // `detected` is fixed for the page's life; `selected` starts equal to it and is
  // changed only by the toggle. Neither is ever read from or written to the URL.
  const [detected] = useState<Platform | null>(() => detectPlatform({ userAgent, maxTouchPoints }));
  const [selected, setSelected] = useState<Platform | null>(detected);

  if (!params.ok) {
    return (
      <main className="setup-page">
        <header className="setup-page__header">
          <h1>{strings.pageHeading}</h1>
        </header>
        <div className="setup-page__error" role="alert">
          <h2>{strings.errorHeading}</h2>
          <p>{strings.errorBody}</p>
        </div>
      </main>
    );
  }

  const { app, displayName, nameIsFallback, user, personalTopic, broadcastTopic } = params;
  // ntfy:// links only work with the ntfy app on this very device, so they render
  // only when the browser itself is Android and Android is the shown tab.
  const oneTap = selected === 'android' && detected === 'android';

  return (
    <main className="setup-page">
      <header className="setup-page__header">
        <h1>{strings.pageHeading}</h1>
        <span
          className={
            nameIsFallback ? 'setup-page__badge setup-page__badge--mono' : 'setup-page__badge'
          }
        >
          {displayName}
        </span>
      </header>
      <p className="setup-page__lead">
        {strings.leadBefore}
        <strong>{displayName}</strong>
        {strings.leadAfterName}
        <strong>{strings.leadNtfy}</strong>
        {strings.leadAfterNtfy}
      </p>
      {detected === null && (
        <QrHandoff app={app} user={user} name={nameIsFallback ? null : displayName} />
      )}
      {/* Static: the same three stores whatever the detected phone or selected tab. */}
      <InstallCard />
      {detected === null && <h2 className="setup-page__section-h">{strings.stepsHere}</h2>}
      <PlatformTabs
        detected={detected}
        selected={selected}
        onSelect={setSelected}
        iosPanel={
          <IosSteps
            displayName={displayName}
            user={user}
            personalTopic={personalTopic}
            broadcastTopic={broadcastTopic}
          />
        }
        androidPanel={
          <AndroidSteps
            displayName={displayName}
            user={user}
            personalTopic={personalTopic}
            broadcastTopic={broadcastTopic}
            oneTap={oneTap}
          />
        }
      />
      <p className="setup-page__footer">{strings.footer}</p>
    </main>
  );
}
