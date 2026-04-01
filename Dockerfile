# ---- Build stage ----
FROM golang:1.25-alpine AS build

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o /bin/gateway  ./cmd/gateway \
 && CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o /bin/keygen   ./cmd/keygen \
 && CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o /bin/migrate  ./cmd/migrate

# ---- Runtime stage ----
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata bash

COPY --from=build /bin/gateway  /usr/local/bin/gateway
COPY --from=build /bin/keygen   /usr/local/bin/keygen
COPY --from=build /bin/migrate  /usr/local/bin/migrate

COPY configs/    /etc/aegis/configs/
COPY migrations/ /etc/aegis/migrations/
COPY deploy/docker-entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENV AEGIS_CONFIG_DIR=/etc/aegis/configs \
    AEGIS_MIGRATIONS_DIR=/etc/aegis/migrations

EXPOSE 8080 9090

ENTRYPOINT ["entrypoint.sh"]
CMD ["gateway"]
