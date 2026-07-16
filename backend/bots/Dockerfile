# Bot Orchestration Service — sin dependencias de runtime: la imagen final
# solo lleva Node y el JS compilado.
FROM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json tsconfig.json ./
RUN npm ci
COPY src ./src
RUN npm run build

FROM node:22-alpine
ENV NODE_ENV=production
WORKDIR /app
USER node
COPY --from=build --chown=node:node /app/dist ./dist
COPY --from=build --chown=node:node /app/package.json ./
# GATEWAY_URL va por entorno (los bots solo hablan con la API pública).
CMD ["node", "dist/orchestrator.js"]
