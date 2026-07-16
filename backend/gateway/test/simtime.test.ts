import { test } from 'node:test';
import assert from 'node:assert/strict';
import { formatSimTime } from '../src/lib/simtime.js';

test('formatSimTime: génesis del mundo', () => {
  assert.equal(formatSimTime(0), '1-001-00:00');
});

test('formatSimTime: horas y minutos dentro del primer día', () => {
  assert.equal(formatSimTime(12 * 3600 + 30 * 60), '1-001-12:30');
});

test('formatSimTime: último día del año 1', () => {
  // día 360 del año 1 = días totales 359
  assert.equal(formatSimTime(359 * 86_400), '1-360-00:00');
});

test('formatSimTime: cambio de año en el día 361 total', () => {
  assert.equal(formatSimTime(360 * 86_400), '2-001-00:00');
});

test('formatSimTime: patrón del spec ^[0-9]{1,4}-[0-9]{3}-[0-9]{2}:[0-9]{2}$', () => {
  const re = /^[0-9]{1,4}-[0-9]{3}-[0-9]{2}:[0-9]{2}$/;
  for (const s of [0, 59, 3600, 86_399, 86_400, 31_104_000, 359 * 86_400 + 82_859]) {
    assert.match(formatSimTime(s), re);
  }
});
