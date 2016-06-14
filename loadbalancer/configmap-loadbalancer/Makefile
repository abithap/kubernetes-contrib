
all: push

TAG = 0.1
PREFIX = kokhang/configmap-loadbalancer

configmap-loadbalancer:
	CGO_ENABLED=0 GOOS=linux godep go build -a -installsuffix cgo -ldflags '-w' -o configmap-loadbalancer *.go

test:
	godep go test ./...

container: clean configmap-loadbalancer test
	docker build -t $(PREFIX):$(TAG) .

push: container
	docker push $(PREFIX):$(TAG)

osx:
	CGO_ENABLED=0 GOOS=darwin godep go build -a -installsuffix cgo -ldflags '-w' -o osx-configmap-loadbalancer *.go

clean:
	rm -f configmap-loadbalancer