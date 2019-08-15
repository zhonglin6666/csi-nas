# Disable make's implicit rules, which are not useful for golang, and slow down the build
# considerably.
.SUFFIXES:

REV=v1.0.0

all: fmt build

fmt:
	gofmt -s -l -w ./cmd/
	gofmt -s -l -w ./pkg/

build:
	mkdir -p bin
	echo "Building csi nas plugin..."
	CGO_ENABLED=0 GOOS=linux go build -v -i -ldflags '-X main.version=$(REV) ' -o ./bin/csi-nas-plugin ./cmd/$*

.PHONY: test

ifeq ($(WHAT),)
    TESTDIR=.
else
    TESTDIR=${WHAT}
endif

ifeq ($(print),y)
    PRINT=-v
endif

test:
	go test ${PRINT} `go list ${TESTDIR}/... | grep -v vendor `

clean:
	rm -rf bin