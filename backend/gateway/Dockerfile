# Gateway API pública (Fastify) — build multi-stage: compila TS y deja solo
# dependencias de producción en la imagen final.
FROM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY tsconfig.json ./
COPY src ./src
RUN npm run build && npm ci --omit=dev

FROM node:22-alpine
ENV NODE_ENV=production
WORKDIR /app
USER node
COPY --from=build --chown=node:node /app/node_modules ./node_modules
COPY --from=build --chown=node:node /app/dist ./dist
COPY --from=build --chown=node:node /app/package.json ./
EXPOSE 8080
# DATABASE_URL y PORT van por entorno.
CMD ["node", "dist/server.js"]
