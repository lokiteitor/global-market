// Rate limit token-bucket in-memory: 300 req/min por cuenta — idéntico para
// humanos y bots (decisión de balance, no solo de protección).
import type { FastifyInstance } from 'fastify';

interface Bucket {
  tokens: number;
  updatedAt: number;
}

export function registerRateLimit(app: FastifyInstance): void {
  const buckets = new Map<string, Bucket>();
  const capacity = app.cfg.rateLimitPerMinute;
  const refillPerMs = capacity / 60_000;

  app.addHook('onRequest', async (req, reply) => {
    if (!req.url.startsWith('/api/')) return;
    if (!req.account) return; // solo se limita por cuenta autenticada
    const now = Date.now();
    let bucket = buckets.get(req.account.id);
    if (!bucket) {
      bucket = { tokens: capacity, updatedAt: now };
      buckets.set(req.account.id, bucket);
    } else {
      bucket.tokens = Math.min(capacity, bucket.tokens + (now - bucket.updatedAt) * refillPerMs);
      bucket.updatedAt = now;
    }
    if (bucket.tokens < 1) {
      const retryAfter = Math.ceil((1 - bucket.tokens) / refillPerMs / 1000);
      await reply
        .status(429)
        .header('Retry-After', String(Math.max(1, retryAfter)))
        .send({
          error: {
            code: 'RATE_LIMITED',
            message: 'Rate limit excedido (300 req/min por cuenta)',
            details: { retry_after_seconds: Math.max(1, retryAfter) },
          },
        });
      return;
    }
    bucket.tokens -= 1;
  });
}
