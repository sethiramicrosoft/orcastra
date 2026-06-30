# syntax=docker/dockerfile:1.7
# Stage 1: Build frontend
FROM node:22-alpine AS frontend
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
ARG VITE_API_BASE=""
RUN npm run build

# Stage 2: Build backend with embedded frontend
FROM golang:1.26-alpine AS builder
WORKDIR /src

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
# Copy built frontend into the embed directory
COPY --from=frontend /web/dist ./cmd/orcastra/ui/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/orcastra ./cmd/orcastra

FROM gcr.io/distroless/static-debian12
WORKDIR /
COPY --from=builder /out/orcastra /orcastra
EXPOSE 3000
ENTRYPOINT ["/orcastra"]
