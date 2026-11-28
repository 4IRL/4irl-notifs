interface AppNameProps {
  displayName: string;
  // Only ever a validated https 4irl.app URL (params.ts), or null.
  returnUrl: string | null;
}

// The app's name inside a sentence. With a validated return address it is a same-tab link
// back to the app (the underline is the only cue); without one it is the plain name.
export function AppName({ displayName, returnUrl }: AppNameProps) {
  if (returnUrl === null) {
    return <>{displayName}</>;
  }
  return (
    <a className="setup-page__app-link" href={returnUrl} rel="noreferrer">
      {displayName}
    </a>
  );
}
