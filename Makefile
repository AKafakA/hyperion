DOCKER_USERNAME=wd312
CTRL_IMAGE_NAME=my-ctl
SCHED_IMAGE_NAME=my-sched
GO_FLAGS=

ifdef RACE
	GO_FLAGS += -race
endif

all: build pushctl pushshed

msg: internal/message/message.proto
	protoc --go_out=. --go_opt=paths=source_relative \
	--go-grpc_out=. --go-grpc_opt=paths=source_relative $<


ctl: cmd/controller/main.go msg
	go build ${GO_FLAGS} -o bin/ctl $<

sched: cmd/scheduler/main.go
	go build ${GO_FLAGS} -o bin/sched $<

build: ctl sched
	docker build -t ${DOCKER_USERNAME}/my-ctl -f build/package/Dockerfile_ctrl . && \
	docker build -t ${DOCKER_USERNAME}/my-sched -f build/package/Dockerfile_sched .


pushctl: 
	docker push ${DOCKER_USERNAME}/my-ctl

pushshed:
	docker push ${DOCKER_USERNAME}/my-sched
