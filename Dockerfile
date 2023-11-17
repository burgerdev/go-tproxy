FROM docker.io/golang:1.21.3 AS build

ADD . /src

RUN cd /src && CGO_ENABLED=0 go build ./example/tproxy_example.go

FROM scratch

COPY --from=build /src/tproxy_example /tproxy

ENTRYPOINT ["/tproxy"]
