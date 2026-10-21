import { NTFY_SERVER } from '../params';
import { strings } from '../strings';
import { CopyField } from './CopyField';
import { Screenshot } from './Screenshot';
import { Step } from './Step';
import './Steps.css';

interface IosStepsProps {
  displayName: string;
  user: string;
  personalTopic: string;
  broadcastTopic: string;
}

// The five iPhone steps (the install card above the tabs covers getting the app). The iOS
// app has no deep link for subscribing, so every value is a CopyField and nothing here
// renders an ntfy:// anchor.
export function IosSteps({ displayName, user, personalTopic, broadcastTopic }: IosStepsProps) {
  return (
    // role="list" restores list semantics that WebKit drops under `list-style: none`.
    <ol className="setup-page__steps" role="list">
      <Step number={1} title={strings.iosServerTitle}>
        <p>
          {strings.iosServerBefore}
          <b>{strings.iosServerBold}</b>
          {strings.iosServerAfter}
        </p>
        <CopyField label={strings.labelDefaultServer} value={NTFY_SERVER} />
        <div className="setup-page__warn">{strings.iosServerWarning}</div>
        <Screenshot name="iosDefaultServer" />
      </Step>
      <Step number={2} title={strings.logInTitle}>
        <p>
          {strings.iosLogInBefore}
          <b>{strings.iosLogInBold}</b>
          {strings.iosLogInAfter}
        </p>
        <CopyField label={strings.labelUsername} value={user} />
        <p className="setup-page__pw">{strings.passwordNote(displayName)}</p>
        <Screenshot name="iosAddUser" />
      </Step>
      <Step number={3} title={strings.iosPersonalTitle}>
        <p>
          {strings.iosPersonalBefore}
          <b>{strings.iosPersonalBold}</b>
          {strings.iosPersonalAfter}
        </p>
        <CopyField label={strings.labelPersonalTopic} value={personalTopic} />
      </Step>
      <Step number={4} title={strings.iosBroadcastTitle}>
        <p>
          {strings.iosBroadcastBefore}
          <b>{strings.iosBroadcastBold}</b>
          {strings.iosBroadcastAfter}
        </p>
        <CopyField label={strings.labelBroadcastTopic} value={broadcastTopic} />
      </Step>
      <Step number={5} title={strings.testItTitle}>
        <p>
          {strings.testItBefore(displayName)}
          <b>{strings.testItBold}</b>
          {strings.testItAfter}
        </p>
      </Step>
    </ol>
  );
}
