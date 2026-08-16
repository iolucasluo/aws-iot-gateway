FROM golang@sha256:3aff6657219a4d9c14e27fb1d8976c49c29fddb70ba835014f477e1c70636647 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/aws-iot-gateway ./cmd/aws-iot-gateway && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/iot-gateway-event-worker ./cmd/event-worker

FROM alpine@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

RUN addgroup -S -g 10001 gateway && adduser -S -D -H -u 10001 -G gateway gateway
COPY --from=build /out/aws-iot-gateway /usr/local/bin/aws-iot-gateway
COPY --from=build /out/iot-gateway-event-worker /usr/local/bin/iot-gateway-event-worker
USER 10001
EXPOSE 14080
CMD ["/usr/local/bin/aws-iot-gateway"]
