FROM golang:1.26.5 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/query-api ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/query-api /query-api

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/query-api"]
