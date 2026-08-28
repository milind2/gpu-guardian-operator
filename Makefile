.PHONY: build test vet run docker deploy

build:
	go build -o bin/manager ./cmd/manager

test:
	go test ./... -cover

vet:
	go vet ./...

# Run locally against `kubectl proxy` (run that in another terminal first:
# kubectl proxy --port=8001).
run: build
	GPU_GUARDIAN_DEV_API_HOST=http://localhost:8001 ./bin/manager

docker:
	docker build -t ghcr.io/milind2/gpu-guardian-operator:latest .

deploy:
	kubectl apply -f deploy/crd.yaml
	kubectl apply -f deploy/rbac.yaml
	kubectl apply -f deploy/deployment.yaml
