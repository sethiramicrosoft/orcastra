# syntax=docker/dockerfile:1.7
FROM golang:1.26-alpine AS builder
WORKDIR /src

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/orcastra ./cmd/orcastra

FROM gcr.io/distroless/static-debian12
WORKDIR /
COPY --from=builder /out/orcastra /orcastra
EXPOSE 3000
ENTRYPOINT ["/orcastra"]
