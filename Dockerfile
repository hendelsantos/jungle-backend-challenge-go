FROM golang:1.27.2-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -S app && adduser -S -G app app
COPY --from=build /out/server /usr/local/bin/server
COPY --from=build /out/migrate /usr/local/bin/migrate
COPY migrations/ /usr/local/share/jungle/migrations/
USER app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/server"]
