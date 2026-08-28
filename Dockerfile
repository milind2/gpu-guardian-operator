FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/manager ./cmd/manager

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
