// Configuración del gateway. Todo valor es sobreescribible por entorno;
// las ventanas de tiempo real siguen ADR-011 (valores dev del rango del GDD 5.3.1).
export interface Config {
  port: number;
  databaseUrl: string;
  drawWindowSeconds: number;
  microWindowSeconds: number;
  cancelCooldownSeconds: number;
  rateLimitPerMinute: number;
  sessionTtlDays: number;
  maintenanceRetryAfterSeconds: number;
  simCacheMs: number;
  outboxPollMs: number;
}

export function loadConfig(): Config {
  return {
    port: Number(process.env.PORT ?? 8080),
    databaseUrl:
      process.env.DATABASE_URL ?? 'postgres://imperio:imperio@localhost:5440/imperio',
    drawWindowSeconds: 45,
    microWindowSeconds: 20,
    cancelCooldownSeconds: 30,
    rateLimitPerMinute: 300,
    sessionTtlDays: 7,
    maintenanceRetryAfterSeconds: 900,
    simCacheMs: 1000,
    outboxPollMs: 1000,
  };
}
