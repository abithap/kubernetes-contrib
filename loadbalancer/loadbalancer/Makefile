
all: push

TAG = 0.1
PREFIX = kokhang/ingress-loadbalancer

ingress-loadbalancer:
	CGO_ENABLED=0 GOOS=linux godep go build -a -installsuffix cgo -ldflags '-w' -o ingress-loadbalancer *.go

test:
	godep go test ./...

container: clean ingress-loadbalancer test
	docker build -t $(PREFIX):$(TAG) .

push: container
	docker push $(PREFIX):$(TAG)

osx:
	CGO_ENABLED=0 GOOS=darwin godep go build -a -installsuffix cgo -ldflags '-w' -o osx-ingress-loadbalancer *.go

clean:
	rm -f ingress-loadbalancer