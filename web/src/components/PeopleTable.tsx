import type { JSX } from 'react';

import { useCompactTable } from '../hooks/useCompactTable';
import { useFocusRegistry } from '../hooks/useFocusRegistry';
import { strings } from '../strings';
import type { PersonSummary } from '../api/personClient';
import { revealFocusedControl } from './adminTableFocus';
import { ConfirmButton } from './ConfirmButton';
import { LocalTime } from './LocalTime';
import { RowActionsMenu } from './RowActionsMenu';
import './adminTable.css';
import './PeopleTable.css';

/** Identifies a person targeted for full-teardown deletion. */
interface DeletePersonParams {
  personHash: string;
}

interface PeopleTableProps {
  people: PersonSummary[];
  loading: boolean;
  error: string | null;
  onDelete: (params: DeletePersonParams) => void;
}

export function PeopleTable({ people, loading, error, onDelete }: PeopleTableProps): JSX.Element {
  const compact = useCompactTable();
  const { registerControl } = useFocusRegistry();
  return (
    <section className="people-table">
      <h2>{strings.peopleHeading}</h2>
      {loading && <p className="people-table__status">{strings.peopleLoading}</p>}
      {!loading && error !== null && (
        <p role="alert" className="people-table__status">
          {error}
        </p>
      )}
      {!loading && error === null && people.length === 0 && (
        <p className="people-table__status">{strings.peopleEmpty}</p>
      )}
      {!loading && error === null && people.length > 0 && (
        <div
          className="admin-table__scroll people-table__scroll"
          onFocus={revealFocusedControl}
          role="group"
          aria-label={strings.peopleHeading}
          tabIndex={0}
        >
          <table className="admin-table people-table__table">
            <thead>
              <tr>
                {compact ? (
                  <>
                    <th scope="col" className="admin-table__lead">
                      {strings.columnPerson}
                    </th>
                    <th scope="col" className="admin-table__fill">
                      {strings.columnCreated}
                    </th>
                  </>
                ) : (
                  <>
                    <th scope="col" className="admin-table__lead">
                      {strings.columnPersonHash}
                    </th>
                    <th scope="col" className="admin-table__fill">
                      {strings.columnEmail}
                    </th>
                    <th scope="col">{strings.columnCreated}</th>
                  </>
                )}
                <th
                  scope="col"
                  className={`admin-table__actions${compact ? ' admin-table__kebab' : ''}`}
                  aria-label={strings.columnActions}
                />
              </tr>
            </thead>
            <tbody>
              {people.map(({ personHash, email, createdAt }) => {
                const deleteLabel = `${strings.deleteAction} ${email}`;
                // Full teardown: deletes the ntfy user (every app grant + token) AND this
                // reverse-index row. Labeled by email so the opaque hash never anchors a
                // destructive action.
                return (
                  <tr key={personHash}>
                    {compact ? (
                      <>
                        <td className="admin-table__lead" title={email}>
                          {email}
                          <span className="admin-table__sub people-table__hash">{personHash}</span>
                        </td>
                        <td className="admin-table__fill">
                          <LocalTime timestamp={createdAt} />
                        </td>
                        <td className="admin-table__actions admin-table__kebab">
                          <RowActionsMenu
                            rowLabel={email}
                            focusKeys={[`delete:${personHash}`]}
                            registerControl={registerControl}
                            items={[
                              {
                                id: 'delete',
                                label: strings.deleteAction,
                                ariaLabel: deleteLabel,
                                tone: 'danger',
                                confirm: {
                                  confirmLabel: strings.confirmDeleteAction,
                                  confirmAriaLabel: `${strings.confirmDeleteAction} ${email}`,
                                  cancelLabel: strings.cancelAction,
                                  cancelAriaLabel: strings.cancelActionNamed({
                                    subject: deleteLabel,
                                  }),
                                  notice: strings.deletePersonNotice,
                                  dialogTitle: strings.deletePersonDialogTitle,
                                  dialogItem: { label: strings.dialogItemPerson, value: email },
                                  onConfirm: () => onDelete({ personHash }),
                                },
                              },
                            ]}
                          />
                        </td>
                      </>
                    ) : (
                      <>
                        <td className="admin-table__lead people-table__hash">{personHash}</td>
                        <td className="admin-table__fill">{email}</td>
                        <td>
                          <LocalTime timestamp={createdAt} />
                        </td>
                        <td className="admin-table__actions">
                          <ConfirmButton
                            triggerLabel={deleteLabel}
                            confirmLabel={`${strings.confirmDeleteAction} ${email}`}
                            dismissAriaLabel={strings.cancelActionNamed({ subject: deleteLabel })}
                            dialogTitle={strings.deletePersonDialogTitle}
                            dialogItem={{ label: strings.dialogItemPerson, value: email }}
                            confirmNotice={strings.deletePersonNotice}
                            className="people-table__button people-table__button--danger"
                            onConfirm={() => onDelete({ personHash })}
                          />
                        </td>
                      </>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
