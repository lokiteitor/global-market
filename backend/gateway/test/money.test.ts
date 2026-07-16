import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  compareAmounts,
  guaranteeTenPct,
  parseAmount,
  parsePositiveAmount,
} from '../src/lib/money.js';
import { AppError } from '../src/lib/errors.js';

test('parseAmount: acepta strings de dígitos y devuelve BigInt', () => {
  assert.equal(parseAmount('0', 'x'), 0n);
  assert.equal(parseAmount('125000', 'x'), 125000n);
  assert.equal(parseAmount('9007199254740993', 'x'), 9007199254740993n); // > 2^53
});

test('parseAmount: rechaza floats, negativos y números JSON', () => {
  for (const bad of ['1.5', '-3', 'abc', '', 100 as unknown, null, undefined]) {
    assert.throws(() => parseAmount(bad, 'x'), AppError);
  }
});

test('parsePositiveAmount: rechaza cero', () => {
  assert.throws(() => parsePositiveAmount('0', 'x'), AppError);
});

test('guaranteeTenPct: 10% con redondeo hacia arriba', () => {
  assert.equal(guaranteeTenPct(1000n), 100n);
  assert.equal(guaranteeTenPct(1001n), 101n); // ceil(100.1)
  assert.equal(guaranteeTenPct(9n), 1n); // ceil(0.9)
  assert.equal(guaranteeTenPct(0n), 0n);
});

test('compareAmounts: comparación numérica, no lexicográfica', () => {
  assert.equal(compareAmounts('9', '10'), -1);
  assert.equal(compareAmounts('100', '100'), 0);
  assert.equal(compareAmounts('200', '30'), 1);
});
