FROM golang:1.27-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/api ./cmd/api
RUN CGO_ENABLED=0 go build -o /out/worker ./cmd/worker

FROM alpine:3 AS api
RUN apk add --no-cache poppler-utils
COPY --from=builder /out/api /usr/local/bin/api
EXPOSE 8080
CMD ["api"]

FROM alpine:3 AS worker
COPY --from=builder /out/worker /usr/local/bin/worker
CMD ["worker"]
