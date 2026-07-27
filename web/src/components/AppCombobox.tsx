import { useId, useState } from 'react';
import type { JSX } from 'react';

import type { AppSummary } from '../api/personClient';
import { strings } from '../strings';
import { isValidAppId } from '../validation';
import './AppCombobox.css';

/** Props for AppCombobox. `value`/`onChange` bind the raw app_id text — free
 *  text is always allowed, so the field degrades to a plain input when `apps`
 *  is empty (registry off / no personClient). */
interface AppComboboxProps {
  id: string;
  value: string;
  onChange: (value: string) => void;
  apps: AppSummary[];
  placeholder?: string;
}

/** One selectable option: a registered app, or the "use this unregistered
 *  app_id as typed" escape hatch. */
type Option = { kind: 'app'; app: AppSummary } | { kind: 'unprovisioned'; value: string };

/**
 * Fluid type-ahead combobox for choosing an app_id on the Provision form. As
 * you type, registered apps filter live; when the typed value is a valid app_id
 * that isn't registered, a "Use unprovisioned app" option is offered so an
 * operator can deliberately provision into an as-yet-unregistered app. The
 * input value is always the raw text, so submitting works whether or not an
 * option was clicked.
 */
export function AppCombobox({
  id,
  value,
  onChange,
  apps,
  placeholder,
}: AppComboboxProps): JSX.Element {
  const [open, setOpen] = useState(false);
  const [highlightedIndex, setHighlightedIndex] = useState(-1);
  const listId = useId();

  const trimmed = value.trim();
  const query = trimmed.toLowerCase();
  const matchedApps = apps.filter(
    (app) =>
      app.appId.toLowerCase().includes(query) || app.displayName.toLowerCase().includes(query),
  );
  // The escape hatch only appears for a well-formed app_id that isn't already a
  // registered app (an exact registered match is offered as a real option).
  const showUnprovisioned = isValidAppId(trimmed) && !apps.some((app) => app.appId === trimmed);

  const options: Option[] = [
    ...matchedApps.map((app): Option => ({ kind: 'app', app })),
    ...(showUnprovisioned ? [{ kind: 'unprovisioned', value: trimmed } as Option] : []),
  ];
  const listboxVisible = open && options.length > 0;
  // The options list can shrink for reasons other than typing — e.g. the `apps`
  // prop (the live registry) shrinking when an app is removed via the sibling
  // Apps table while this popover is open. `highlightedIndex` (state) isn't
  // reset in that case, so clamp it here: a stale index must never index past
  // `options` (Enter would then commit `undefined` and throw) or point
  // aria-activedescendant at a non-existent option.
  const activeIndex = highlightedIndex < options.length ? highlightedIndex : -1;

  function commit(option: Option) {
    onChange(option.kind === 'app' ? option.app.appId : option.value);
    setOpen(false);
    setHighlightedIndex(-1);
  }

  function handleKeyDown(event: React.KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'Escape') {
      setOpen(false);
      setHighlightedIndex(-1);
      return;
    }
    if (event.key === 'ArrowDown' && options.length > 0) {
      event.preventDefault();
      setOpen(true);
      setHighlightedIndex((prev) => (prev + 1) % options.length);
      return;
    }
    if (event.key === 'ArrowUp' && options.length > 0) {
      event.preventDefault();
      setOpen(true);
      setHighlightedIndex((prev) => (prev - 1 + options.length) % options.length);
      return;
    }
    if (event.key === 'Enter' && listboxVisible && activeIndex >= 0) {
      // Commit the highlighted option instead of submitting the form; with no
      // highlight, Enter falls through so the form submits the typed value.
      event.preventDefault();
      commit(options[activeIndex]);
    }
  }

  const optionDomId = (index: number) => `${listId}-opt-${index}`;

  return (
    <div className="app-combobox">
      <input
        id={id}
        type="text"
        className="app-combobox__input"
        role="combobox"
        aria-expanded={listboxVisible}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={activeIndex >= 0 ? optionDomId(activeIndex) : undefined}
        placeholder={placeholder}
        value={value}
        onChange={(event) => {
          onChange(event.target.value);
          setOpen(true);
          setHighlightedIndex(-1);
        }}
        onFocus={() => setOpen(true)}
        // onMouseDown(preventDefault) on options keeps focus, so a click commits
        // before this blur closes the popover.
        onBlur={() => setOpen(false)}
        onKeyDown={handleKeyDown}
      />
      {listboxVisible && (
        <ul className="app-combobox__list" id={listId} role="listbox">
          {options.map((option, index) => {
            const highlighted = index === activeIndex;
            const className =
              'app-combobox__option' +
              (option.kind === 'unprovisioned' ? ' app-combobox__option--unprovisioned' : '') +
              (highlighted ? ' app-combobox__option--highlighted' : '');
            return (
              <li
                key={option.kind === 'app' ? `app-${option.app.appId}` : 'unprovisioned'}
                id={optionDomId(index)}
                role="option"
                aria-selected={highlighted}
                className={className}
                onMouseDown={(event) => {
                  event.preventDefault();
                  commit(option);
                }}
                onMouseMove={() => setHighlightedIndex(index)}
              >
                {option.kind === 'app' ? (
                  <>
                    <span className="app-combobox__option-id">{option.app.appId}</span>
                    <span className="app-combobox__option-name">{option.app.displayName}</span>
                  </>
                ) : (
                  strings.useUnprovisionedApp({ value: option.value })
                )}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
