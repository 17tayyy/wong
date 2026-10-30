FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go generate ./internal/dashboard
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.release=${VERSION}" -o /wong ./cmd/wong && mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source=https://github.com/17tayyy/wong
COPY --from=build /wong /wong
COPY --from=build --chown=nonroot:nonroot /data /data
WORKDIR /data
VOLUME /data
EXPOSE 8787
ENTRYPOINT ["/wong"]
CMD ["serve"]
