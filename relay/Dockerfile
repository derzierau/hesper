FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Deploy names: the binaries keep their names ghosty-relay and ghostyctl in
# the image, as in deploy/Dockerfile.prebuilt, until the next relay deploy.
RUN CGO_ENABLED=0 go build -trimpath -o /out/ghosty-relay ./cmd/hesper-relay \
 && CGO_ENABLED=0 go build -trimpath -o /out/ghostyctl ./cmd/hesperctl

FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
 && addgroup -g 10001 ghosty && adduser -D -u 10001 -G ghosty ghosty \
 && mkdir /data && chown ghosty:ghosty /data
COPY --from=build /out/ /usr/local/bin/
USER ghosty
EXPOSE 8787
ENTRYPOINT ["ghosty-relay"]
CMD ["--listen", "0.0.0.0:8787", "--data", "/data"]
