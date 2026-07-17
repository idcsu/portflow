# syntax=docker/dockerfile:1
FROM node:22-alpine AS build

WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM caddy:2.8-alpine
COPY deploy/web/Caddyfile /etc/caddy/Caddyfile
COPY --from=build /src/web/dist /srv
