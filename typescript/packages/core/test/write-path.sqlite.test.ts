import { describeWritePath } from './support/write-path';
import { openSqlite } from './support/sqlite';

describe('write path on SQLite', () => {
  describeWritePath('sqlite', async () => {
    const opened = await openSqlite();
    return { ...opened, concurrent: opened.prisma };
  });
});
