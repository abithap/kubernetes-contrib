
all: push

TAG = 0.1
PREFIX = kokhang/loadbalancer-daemon

loadbalancer-daemon:
	CGO_ENABLED=0 GOOS=linux godep go build -a -installsuffix cgo -ldflags '-w' -o loadbalancer-daemon *.go

test:
	godep go test ./...

container: clean loadbalancer-daemon test
	docker build -t $(PREFIX):$(TAG) .

push: container
	docker push $(PREFIX):$(TAG)

osx:
	CGO_ENABLED=0 GOOS=darwin godep go build -a -installsuffix cgo -ldflags '-w' -o osx-loadbalancer-daemon *.go

clean:
	rm -f loadbalancer-daemon