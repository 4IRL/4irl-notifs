import { GOOGLE_PLAY_URL, subscribeHref } from '../ntfyLinks';
import { NTFY_SERVER } from '../params';
import { strings } from '../strings';
import { CopyField } from './CopyField';
import { Screenshot } from './Screenshot';
import { Step } from './Step';
import { StoreLink } from './StoreLink';
import './Steps.css';

interface AndroidStepsProps {
  displayName: string;
  user: string;
  personalTopic: string;
  broadcastTopic: string;
  // True only when the detected platform is Android and Android is selected. The
  // ntfy:// anchors need the ntfy app on this very device, so they never render
  // for a desktop visitor or an iPhone user who switched tabs.
  oneTap: boolean;
}

// The four Android steps. Both topics always get a CopyField (issue #46); the
// one-tap variant adds ntfy:// anchors above them.
export function AndroidSteps({
  displayName,
  user,
  personalTopic,
  broadcastTopic,
  oneTap,
}: AndroidStepsProps) {
  return (
    <ol className="setup-page__steps" role="list">
      <Step number={1} title={strings.installNtfyTitle}>
        <StoreLink href={GOOGLE_PLAY_URL} label={strings.googlePlayLink} />
      </Step>
      <Step number={2} title={strings.logInTitle}>
        <p>
          {strings.androidLogInBefore}
          <b>{strings.androidLogInBold}</b>
          {strings.androidLogInAfter}
        </p>
        <CopyField label={strings.labelServiceUrl} value={NTFY_SERVER} />
        <CopyField label={strings.labelUsername} value={user} />
        <p className="setup-page__pw">{strings.passwordNote(displayName)}</p>
        <Screenshot name="androidAddUser" />
      </Step>
      {oneTap ? (
        <Step number={3} title={strings.androidOneTapTitle}>
          <div className="setup-page__subscribe-group">
            <a className="setup-page__primary" href={subscribeHref(personalTopic)}>
              {strings.androidOneTapPersonal}
            </a>
            <p className="setup-page__fallback">
              {strings.androidFallbackPersonalBefore}
              <b>{strings.androidFallbackBold}</b>
              {strings.androidFallbackPersonalAfter}
            </p>
            <CopyField label={strings.labelPersonalTopic} value={personalTopic} />
          </div>
          <div className="setup-page__subscribe-group">
            <a
              className="setup-page__primary setup-page__primary--secondary"
              href={subscribeHref(broadcastTopic)}
            >
              {strings.androidOneTapBroadcast}
            </a>
            <p className="setup-page__fallback">{strings.androidFallbackBroadcast}</p>
            <CopyField label={strings.labelBroadcastTopic} value={broadcastTopic} />
          </div>
        </Step>
      ) : (
        <Step number={3} title={strings.androidCopyTitle}>
          <p>
            {strings.androidCopyBefore}
            <b>{strings.androidCopyBold}</b>
            {strings.androidCopyAfter}
          </p>
          <CopyField label={strings.labelPersonalTopic} value={personalTopic} />
          <CopyField label={strings.labelBroadcastTopic} value={broadcastTopic} />
          <p className="setup-page__fallback">{strings.androidCopyOnPhoneNote}</p>
        </Step>
      )}
      <Step number={4} title={strings.testItTitle}>
        <p>
          {strings.testItBefore(displayName)}
          <b>{strings.testItBold}</b>
          {strings.testItAfter}
        </p>
      </Step>
    </ol>
  );
}
