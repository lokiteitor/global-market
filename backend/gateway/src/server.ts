// Punto de entrada: escucha en PORT (8080 por defecto, tras Caddy en dev).
import { buildApp } from './app.js';

async function main(): Promise<void> {
  const app = await buildApp();
  const port = app.cfg.port;
  try {
    await app.listen({ port, host: '0.0.0.0' });
  } catch (err) {
    app.log.error(err);
    process.exit(1);
  }
  const shutdown = (): void => {
    void app.close().then(() => process.exit(0));
  };
  process.on('SIGINT', shutdown);
  process.on('SIGTERM', shutdown);
}

void main();
