FROM alpine:latest AS builder

# system dependencies
RUN apk add --no-cache \
    protobuf \
    protobuf-dev \
    protoc \
    make \
    go \
    libcap

ENV GOPATH=/root/go
ENV PATH="/root/go/bin:$PATH"
WORKDIR /usr/local/src

# code gen
RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@latest && \
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest && \
    go install github.com/pressly/goose/v3/cmd/goose@latest

# app dependencies
COPY go.mod go.sum ./
RUN go mod download

# build
COPY . .
RUN make proto && \
    go build -o lunar-tear ./cmd/lunar-tear && \
    setcap cap_net_bind_service=+ep ./lunar-tear

# runner
FROM alpine:latest

WORKDIR /opt/lunar-tear

RUN chown 1000:1000 /opt/lunar-tear

USER 1000

COPY --from=builder /usr/local/src/lunar-tear .
COPY --from=builder /root/go/bin/goose /usr/local/bin/goose
COPY --from=builder /usr/local/src/migrations ./migrations

COPY entrypoint.sh .

ENTRYPOINT /opt/lunar-tear/entrypoint.sh

