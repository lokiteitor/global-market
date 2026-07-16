# Cliente web (Nuxt 4) — build multi-stage: la imagen final solo lleva el
# output de Nitro (servidor SSR autocontenido).
FROM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM node:22-alpine
ENV NODE_ENV=production NITRO_HOST=0.0.0.0 NITRO_PORT=3000
WORKDIR /app
USER node
COPY --from=build --chown=node:node /app/.output ./.output
EXPOSE 3000
# Overrides de runtime (opcionales): NUXT_PUBLIC_API_BASE, NUXT_PUBLIC_WS_URL.
CMD ["node", ".output/server/index.mjs"]
