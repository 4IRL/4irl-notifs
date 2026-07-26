import type { JSX } from 'react';

import { strings } from '../strings';
import type { PersonSummary } from '../api/personClient';
import { ConfirmButton } from './ConfirmButton';
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
        <table className="people-table__table">
          <thead>
            <tr>
              <th scope="col">{strings.columnPersonHash}</th>
              <th scope="col">{strings.columnEmail}</th>
              <th scope="col">{strings.columnCreated}</th>
              <th scope="col" aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {people.map(({ personHash, email, createdAt }) => (
              <tr key={personHash}>
                <td className="people-table__hash">{personHash}</td>
                <td>{email}</td>
                <td>{createdAt}</td>
                <td>
                  {/* Full teardown: deletes the ntfy user (every app grant +
                      token) AND this reverse-index row. Labeled by email so the
                      opaque hash never anchors a destructive action. */}
                  <ConfirmButton
                    triggerLabel={`${strings.deleteAction} ${email}`}
                    confirmLabel={`${strings.confirmDeleteAction} ${email}`}
                    className="people-table__button people-table__button--danger"
                    onConfirm={() => onDelete({ personHash })}
                  />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
