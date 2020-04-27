FROM golang:1.14 as builder

WORKDIR /go/src/github.com/barthv/alertmanager-deadman-receiver
COPY    . .
RUN     make

FROM alpine:latest
COPY --from=builder /go/src/github.com/barthv/alertmanager-deadman-receiver/alertmanager-deadman-receiver /alertmanager-deadman-receiver
CMD ["/alertmanager-deadman-receiver"]