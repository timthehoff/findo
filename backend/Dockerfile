FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /findo-server ./cmd/findo-server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /findo-server /usr/local/bin/findo-server
EXPOSE 8080
ENTRYPOINT ["findo-server"]
