// Envoltura { data, meta } de toda respuesta exitosa (specs/openapi.yaml).
// meta.sim_time es el formato legible; meta.sim_time_seconds la forma canónica.
import type { FastifyReply, FastifyRequest } from 'fastify';
import { formatSimTime } from './simtime.js';
import type { SimState } from '../types.js';

export interface Meta {
  sim_time: string;
  sim_time_seconds: number;
  server_time: string;
  next_cursor?: string;
}

export function buildMeta(sim: SimState, nextCursor?: string): Meta {
  const meta: Meta = {
    sim_time: formatSimTime(sim.simSeconds),
    sim_time_seconds: sim.simSeconds,
    server_time: new Date().toISOString(),
  };
  if (nextCursor !== undefined) meta.next_cursor = nextCursor;
  return meta;
}

export async function sendData(
  req: FastifyRequest,
  reply: FastifyReply,
  data: unknown,
  opts: { status?: number; nextCursor?: string } = {},
): Promise<void> {
  const sim = await req.server.getSim();
  await reply
    .status(opts.status ?? 200)
    .send({ data, meta: buildMeta(sim, opts.nextCursor) });
}

/** Elimina claves con valor undefined (los DTO omiten campos ausentes). */
export function clean<T extends Record<string, unknown>>(obj: T): T {
  for (const k of Object.keys(obj)) {
    if (obj[k] === undefined) delete obj[k];
  }
  return obj;
}

export function iso(v: unknown): string | undefined {
  if (v === null || v === undefined) return undefined;
  if (v instanceof Date) return v.toISOString();
  return String(v);
}
