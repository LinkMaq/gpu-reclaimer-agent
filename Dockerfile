# syntax=docker/dockerfile:1

FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod ./
COPY go.sum ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -o /out/gpu-reclaimer-agent ./cmd/gpu-reclaimer-agent

# distroless base (glibc) helps NVML dynamic linking
FROM gcr.io/distroless/base-debian12:nonroot
WORKDIR /
COPY --from=build /out/gpu-reclaimer-agent /gpu-reclaimer-agent
USER nonroot:nonroot
ENTRYPOINT ["/gpu-reclaimer-agent"]
