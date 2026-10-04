import { describeWritePath } from './support/write-path';
import { openSqlite } from './support/sqlite';

describe('write path on SQLite', () => {
  describeWritePath('sqlite', openSqlite);
});
