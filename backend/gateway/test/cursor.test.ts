import { test } from 'node:test';
import assert from 'node:assert/strict';
import { decodeCursor, encodeCursor, parseLimit } from '../src/lib/cursor.js';
import { AppError } from '../src/lib/errors.js';

test('cursor: ida y vuelta', () => {
  for (const offset of [0, 1, 50, 12345]) {
    assert.equal(decodeCursor(encodeCursor(offset)), offset);
  }
});

test('cursor: ausente → offset 0', () => {
  assert.equal(decodeCursor(undefined), 0);
  assert.equal(decodeCursor(''), 0);
});

test('cursor: corrupto → 400', () => {
  assert.throws(() => decodeCursor('no-es-base64url-valido!!'), AppError);
  assert.throws(() => decodeCursor(Buffer.from('x:1').toString('base64url')), AppError);
});

test('parseLimit: default 50, rangos 1..200', () => {
  assert.equal(parseLimit(undefined), 50);
  assert.equal(parseLimit('25'), 25);
  assert.throws(() => parseLimit('0'), AppError);
  assert.throws(() => parseLimit('201'), AppError);
  assert.throws(() => parseLimit('abc'), AppError);
});
