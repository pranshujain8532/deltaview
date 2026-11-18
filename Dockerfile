# Builds deltaview and dvtest (used as the demo load generator).
# pg_query_go needs cgo; the binaries are linked statically so the runtime
# image needs no libc.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -tags netgo,osusergo -trimpath \
      -ldflags '-s -w -extldflags "-static"' -o /out/ ./cmd/deltaview ./cmd/dvtest

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/deltaview /out/dvtest /usr/local/bin/
COPY views.sql /app/views.sql
WORKDIR /app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/deltaview"]
CMD ["--views", "/app/views.sql", "--state-dir", "/data", "--http", ":8080"]
