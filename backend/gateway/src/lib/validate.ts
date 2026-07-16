// Validación manual estricta (la spec es la fuente; sin codegen de schemas).
import { badRequest, notFound } from './errors.js';

const UUID_RE = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;

export function isUuid(v: unknown): v is string {
  return typeof v === 'string' && UUID_RE.test(v);
}

/** Un UUID de path malformado o no resuelto es un 404 (contrato de la API). */
export function uuidParam(v: unknown, _name: string): string {
  if (!isUuid(v)) throw notFound();
  return v;
}

/** UUID de body/query: malformado es un 400. */
export function uuidField(v: unknown, field: string): string {
  if (!isUuid(v)) throw badRequest(`El campo '${field}' debe ser un UUID`);
  return v;
}

export function optionalUuidField(v: unknown, field: string): string | undefined {
  if (v === undefined || v === null || v === '') return undefined;
  return uuidField(v, field);
}

export function simTimeField(v: unknown, field: string): number {
  if (typeof v !== 'number' || !Number.isSafeInteger(v) || v < 0) {
    throw badRequest(`El campo '${field}' debe ser un entero de sim-time >= 0`);
  }
  return v;
}

export function optionalSimTimeQuery(v: unknown, field: string): number | undefined {
  if (v === undefined || v === null || v === '') return undefined;
  const n = Number(v);
  if (!Number.isSafeInteger(n) || n < 0) {
    throw badRequest(`El parámetro '${field}' debe ser un entero de sim-time >= 0`);
  }
  return n;
}

export function intField(v: unknown, field: string, min: number): number {
  if (typeof v !== 'number' || !Number.isInteger(v) || v < min) {
    throw badRequest(`El campo '${field}' debe ser un entero >= ${min}`);
  }
  return v;
}

export function stringField(v: unknown, field: string): string {
  if (typeof v !== 'string' || v.length === 0) {
    throw badRequest(`El campo '${field}' es obligatorio`);
  }
  return v;
}

export function enumField<T extends string>(v: unknown, field: string, values: readonly T[]): T {
  if (typeof v !== 'string' || !(values as readonly string[]).includes(v)) {
    throw badRequest(`El campo '${field}' debe ser uno de: ${values.join(', ')}`);
  }
  return v as T;
}

export function optionalEnumQuery<T extends string>(
  v: unknown,
  field: string,
  values: readonly T[],
): T | undefined {
  if (v === undefined || v === null || v === '') return undefined;
  return enumField(v, field, values);
}

export function body(v: unknown): Record<string, unknown> {
  if (v === null || typeof v !== 'object' || Array.isArray(v)) {
    throw badRequest('El cuerpo de la petición debe ser un objeto JSON');
  }
  return v as Record<string, unknown>;
}

/** GeoJSON Polygon mínimo válido (anillo exterior cerrado). Devuelve el JSON. */
export function geoPolygonField(v: unknown, field: string): string {
  if (
    v === null ||
    typeof v !== 'object' ||
    (v as { type?: unknown }).type !== 'Polygon' ||
    !Array.isArray((v as { coordinates?: unknown }).coordinates)
  ) {
    throw badRequest(`El campo '${field}' debe ser un GeoJSON Polygon`);
  }
  const rings = (v as { coordinates: unknown[] }).coordinates;
  if (rings.length === 0) throw badRequest(`El campo '${field}' debe tener al menos un anillo`);
  for (const ring of rings) {
    if (!Array.isArray(ring) || ring.length < 4) {
      throw badRequest(`El campo '${field}' tiene un anillo inválido (mínimo 4 posiciones)`);
    }
    for (const pos of ring) {
      if (
        !Array.isArray(pos) ||
        pos.length < 2 ||
        typeof pos[0] !== 'number' ||
        typeof pos[1] !== 'number'
      ) {
        throw badRequest(`El campo '${field}' tiene coordenadas inválidas`);
      }
    }
  }
  return JSON.stringify(v);
}
