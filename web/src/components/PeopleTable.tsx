import type { JSX } from 'react';

import { strings } from '../strings';
import type { PersonSummary } from '../api/personClient';
import './PeopleTable.css';

interface PeopleTableProps {
  people: PersonSummary[];
  loading: boolean;
  error: string | null;
}

export function PeopleTable({ people, loading, error }: PeopleTableProps): JSX.Element {
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
            </tr>
          </thead>
          <tbody>
            {people.map(({ personHash, email, createdAt }) => (
              <tr key={personHash}>
                <td className="people-table__hash">{personHash}</td>
                <td>{email}</td>
                <td>{createdAt}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
