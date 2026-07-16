// Errores de dominio con código estable (specs/openapi.yaml → Error.code) y
// estado HTTP asociado. El handler global serializa { error: { code, message, details } }.
export class AppError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
    public readonly details?: Record<string, unknown>,
  ) {
    super(message);
    this.name = 'AppError';
  }
}

export const notFound = (message = 'Entidad inexistente'): AppError =>
  new AppError(404, 'NOT_FOUND', message);

export const unauthorized = (message = 'Sesión ausente o expirada'): AppError =>
  new AppError(401, 'UNAUTHORIZED', message);

export const forbidden = (
  message = 'El recurso pertenece a otra corporación',
  code = 'NOT_RESOURCE_OWNER',
): AppError => new AppError(403, code, message);

export const validation = (
  message: string,
  details?: Record<string, unknown>,
  status = 422,
): AppError => new AppError(status, 'VALIDATION_ERROR', message, details);

export const badRequest = (message: string, details?: Record<string, unknown>): AppError =>
  new AppError(400, 'VALIDATION_ERROR', message, details);

export const conflict = (code: string, message: string, details?: Record<string, unknown>): AppError =>
  new AppError(409, code, message, details);

export const insufficient = (
  code: 'INSUFFICIENT_FUNDS' | 'INSUFFICIENT_COLLATERAL',
  message: string,
  details?: Record<string, unknown>,
): AppError => new AppError(422, code, message, details);

interface PgError {
  code?: string;
  constraint?: string;
}

/**
 * Traduce errores de PostgreSQL a errores de dominio. Los triggers de la base
 * son la garantía final (ADR-005): el CHECK ck_accounts_non_negative aborta
 * cualquier asiento que dejara un saldo negativo — según el contexto del
 * comando se presenta como falta de fondos o de colateral.
 */
export function mapPgError(
  err: unknown,
  insufficientCode: 'INSUFFICIENT_FUNDS' | 'INSUFFICIENT_COLLATERAL' = 'INSUFFICIENT_FUNDS',
): unknown {
  const pg = err as PgError;
  if (pg && typeof pg === 'object' && typeof pg.code === 'string') {
    if (pg.code === '23514' && pg.constraint === 'ck_accounts_non_negative') {
      return new AppError(
        422,
        insufficientCode,
        insufficientCode === 'INSUFFICIENT_FUNDS'
          ? 'Saldo insuficiente para la operación'
          : 'La garantía disponible no cubre la operación solicitada',
      );
    }
    if (pg.code === '23514' || pg.code === '23502' || pg.code === '22P02') {
      return validation('El comando viola una invariante del dominio');
    }
    if (pg.code === '23505') {
      return conflict('VALIDATION_ERROR', 'Conflicto de estado (registro duplicado)');
    }
  }
  return err;
}
