# Build a static binary, then ship it on a base with nothing else in it.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/loadgun ./cmd/loadgun

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/loadgun /loadgun
USER nonroot:nonroot
ENTRYPOINT ["/loadgun"]
