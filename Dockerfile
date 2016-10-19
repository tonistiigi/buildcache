FROM golang:1.7-alpine
RUN apk add --no-cache git
RUN go get --v github.com/tonistiigi/buildcache/cmd/buildcache
ENTRYPOINT ["/go/bin/buildcache"]
CMD ["save", "-h"]
