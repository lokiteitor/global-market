# ══════════════════════════════════════════════════════════════════════════
# Imperio Industrial — Frontend (Nuxt 4, ADR-021), multi-stage
#
# Contexto de build: /frontend (ver infra/docker-compose.yml).
# NOTA: /frontend aún no existe; este Dockerfile queda preparado para el
# perfil full cuando llegue el cliente.
# ══════════════════════════════════════════════════════════════════════════
FROM node:22-alpine AS builder
WORKDIR /src

# Cachear dependencias por separado del código
COPY package.json package-lock.json ./
RUN npm ci

COPY . .
RUN npm run build

# Runtime: servidor Nitro de Nuxt, usuario no root (node ya existe en la imagen)
FROM node:22-alpine
ENV NODE_ENV=production \
    NITRO_PORT=3000 \
    NITRO_HOST=0.0.0.0
WORKDIR /app
COPY --from=builder --chown=node:node /src/.output ./.output
USER node
EXPOSE 3000
CMD ["node", ".output/server/index.mjs"]
