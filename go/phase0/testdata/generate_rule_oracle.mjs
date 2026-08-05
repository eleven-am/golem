// Recomputes the TypeScript/CASL verdict digests embedded in policy_test.go.
// Run from any directory with:
//   node go/phase0/testdata/generate_rule_oracle.mjs

import crypto from 'node:crypto';
import { createRequire } from 'node:module';

const require = createRequire(new URL('../../../typescript/package.json', import.meta.url));
const { createAbility } = require('@eleven-am/authorizer/prisma');
const { subject } = require('@casl/ability');

function chains(alphabet, maxDepth) {
  const all = [[]];
  let level = [[]];
  for (let depth = 1; depth <= maxDepth; depth += 1) {
    level = level.flatMap((chain) => alphabet.map((entry) => [...chain, entry]));
    all.push(...level);
  }
  return all;
}

function digest(lines) {
  return crypto.createHash('sha256').update(lines.join('')).digest('hex');
}

const rowAlphabet = [
  ['A', { action: 'read', subject: 'Post' }],
  ['D', { action: 'read', subject: 'Post', inverted: true }],
  ['O', { action: 'read', subject: 'Post', conditions: { authorId: 'u1' } }],
  ['X', { action: 'read', subject: 'Post', conditions: { authorId: 'u1' }, inverted: true }],
  ['P', { action: 'read', subject: 'Post', conditions: { published: true } }],
  ['N', { action: 'read', subject: 'Post', conditions: { published: false }, inverted: true }],
];
const postRows = [
  { authorId: 'u1', published: false },
  { authorId: 'u1', published: true },
  { authorId: 'u2', published: false },
  { authorId: 'u2', published: true },
];
const rowLines = chains(rowAlphabet, 3).map((chain) => {
  const ability = createAbility(chain.map((entry) => entry[1]));
  const bits = postRows
    .map((row) => (ability.can('read', subject('Post', row)) ? '1' : '0'))
    .join('');
  return `${chain.map((entry) => entry[0]).join('') || '-'}:${bits}\n`;
});

const fieldAlphabet = [
  ['M', { action: 'read', subject: 'User' }],
  ['m', { action: 'read', subject: 'User', inverted: true }],
  ['E', { action: 'read', subject: 'User', fields: ['email'] }],
  ['e', { action: 'read', subject: 'User', fields: ['email'], inverted: true }],
  ['S', { action: 'read', subject: 'User', fields: ['email'], conditions: { id: 'u1' } }],
  ['s', {
    action: 'read',
    subject: 'User',
    fields: ['email'],
    conditions: { id: 'u1' },
    inverted: true,
  }],
];
const userRows = [{ id: 'u1' }, { id: 'u2' }];
const fieldLines = chains(fieldAlphabet, 3).map((chain) => {
  const ability = createAbility(chain.map((entry) => entry[1]));
  const bits = [
    ...userRows.map((row) => ability.can('read', subject('User', row))),
    ...userRows.map((row) => ability.can('read', subject('User', row), 'email')),
    ...userRows.map((row) => ability.can('read', subject('User', row), 'name')),
  ].map((answer) => (answer ? '1' : '0')).join('');
  return `${chain.map((entry) => entry[0]).join('') || '-'}:${bits}\n`;
});

process.stdout.write(`${JSON.stringify({
  maxDepth: 3,
  rowCases: rowLines.length,
  rowSHA256: digest(rowLines),
  fieldCases: fieldLines.length,
  fieldSHA256: digest(fieldLines),
}, null, 2)}\n`);
