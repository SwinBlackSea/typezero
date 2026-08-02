FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/typezero-server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/typezero-server /typezero-server
EXPOSE 8080
ENTRYPOINT ["/typezero-server"]
