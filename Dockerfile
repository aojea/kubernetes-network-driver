ARG GOARCH="amd64"

FROM golang:1.22 AS builder
# golang envs
ARG GOARCH="amd64"
ARG GOOS=linux
ENV CGO_ENABLED=0

WORKDIR /go/src/app
COPY . .
RUN go mod download
RUN CGO_ENABLED=0 go build -o /go/bin/driver .

FROM gcr.io/distroless/base-debian12
COPY --from=builder --chown=root:root /go/bin/driver /driver
CMD ["/driver"]
