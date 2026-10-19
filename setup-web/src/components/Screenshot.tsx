import { screenshots } from '../screenshots';
import type { ScreenshotKey } from '../screenshots';
import './Steps.css';

interface ScreenshotProps {
  name: ScreenshotKey;
}

// A first-party screenshot from the typed manifest. Explicit width/height let the
// browser reserve the space before the image loads; lazy loading keeps the other
// platform's hidden panel from fetching its images up front.
export function Screenshot({ name }: ScreenshotProps) {
  const { src, alt, width, height } = screenshots[name];
  return (
    <img
      className="setup-page__shot"
      src={src}
      alt={alt}
      width={width}
      height={height}
      loading="lazy"
    />
  );
}
