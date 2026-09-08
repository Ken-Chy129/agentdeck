FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/skillhubd ./cmd/skillhubd

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 skillhub
USER skillhub
WORKDIR /data
VOLUME /data
ENV SKILLHUB_ADDR=0.0.0.0:8480 SKILLHUB_DATA=/data
EXPOSE 8480
COPY --from=build /out/skillhubd /usr/local/bin/skillhubd
HEALTHCHECK CMD wget -qO- http://127.0.0.1:8480/healthz || exit 1
ENTRYPOINT ["skillhubd"]
