FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /findo-server ./cmd/findo-server
# distroless has no shell, so create the data dir (owned by its nonroot uid)
# here; a named volume mounted at /data inherits this ownership.
RUN mkdir /data && chown 65532:65532 /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /findo-server /usr/local/bin/findo-server
COPY --from=build --chown=65532:65532 /data /data
ENV DB_PATH=/data/findo.db
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/findo-server"]
