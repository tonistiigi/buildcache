binary:
	go build ./cmd/buildcache

install: binary
	cp ./buildcache /usr/bin

fmt:
	go fmt ./...

vendor: vendor.conf
	vndr
	
.PHONY:
	vendor binary install fmt