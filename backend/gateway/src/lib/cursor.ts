// Cursor opaco de paginación: base64 de un offset. El contrato solo promete
// opacidad, no forma; un cursor corrupto es un 400.
import { badRequest } from './errors.js';

export function encodeCursor(offset: number): string {
  return Buffer.from(`o:${offset}`, 'utf8').toString('base64url');
}

export function decodeCursor(cursor: unknown): number {
  if (cursor === undefined || cursor === null || cursor === '') return 0;
  if (typeof cursor !== 'string') throw badRequest('Cursor de paginación inválido');
  try {
    const raw = Buffer.from(cursor, 'base64url').toString('utf8');
    const m = /^o:(\d+)$/.exec(raw);
    if (!m || m[1] === undefined) throw new Error('bad cursor');
    const offset = Number(m[1]);
    if (!Number.isSafeInteger(offset) || offset < 0) throw new Error('bad cursor');
    return offset;
  } catch {
    throw badRequest('Cursor de paginación inválido');
  }
}

export function parseLimit(value: unknown): number {
  if (value === undefined || value === null || value === '') return 50;
  const n = Number(value);
  if (!Number.isInteger(n) || n < 1 || n > 200) {
    throw badRequest('limit debe ser un entero entre 1 y 200');
  }
  return n;
}
