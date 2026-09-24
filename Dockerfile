FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/spiffe-whoami .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/spiffe-whoami /spiffe-whoami
USER nonroot
EXPOSE 8080 8443
ENTRYPOINT ["/spiffe-whoami"]
