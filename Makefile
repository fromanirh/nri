#   Copyright The containerd Authors.

#   Licensed under the Apache License, Version 2.0 (the "License");
#   you may not use this file except in compliance with the License.
#   You may obtain a copy of the License at

#       http://www.apache.org/licenses/LICENSE-2.0

#   Unless required by applicable law or agreed to in writing, software
#   distributed under the License is distributed on an "AS IS" BASIS,
#   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#   See the License for the specific language governing permissions and
#   limitations under the License.

PROTO_SOURCES = $(shell find . -name '*.proto' | grep -v /vendor/)
PROTO_GOFILES = $(patsubst %.proto,%.pb.go,$(PROTO_SOURCES))
PROTO_INCLUDE = $(HOME)/go/src $(shell go env GOPATH)/src
PROTO_MODULES = # gogoproto/gogo.proto=github.com/gogo/protobuf/gogoproto

TTRPC_INCLUDE = $(foreach dir,$(PROTO_INCLUDE),-I$(dir))
TTRPC_MODULES = $(foreach mod,$(PROTO_MODULES),--gogottrpc_opt=M$(mod))
TTRPC_OPTIONS = $(TTRPC_INCLUDE) $(TTRPC_MODULES) --gogottrpc_opt=paths=source_relative
TTRPC_COMPILE = protoc $(TTRPC_OPTIONS)

GO_CMD   := go
GO_BUILD := $(GO_CMD) build

PLUGINS := bin/logger bin/cpumanager

all: build

build: protos binaries
	go build -v $(shell go list ./...)

protos: $(PROTO_GOFILES)

binaries: $(PLUGINS)

%.pb.go: %.proto
	@echo "Generating $@..."; \
        PATH=$(PATH):$(shell go env GOPATH)/bin; \
	$(TTRPC_COMPILE) -I$(dir $<) --gogottrpc_out=plugins=ttrpc:$(dir $<) $<

bin/logger: $(wildcard v2alpha1/plugins/logger/*.go)
	@echo "Building $@..."; \
	$(GO_BUILD) -o $@ ./$(dir $<)

bin/cpumanager: $(wildcard v2alpha1/plugins/cpumanager/*.go)
	@echo "Building $@..."; \
	$(GO_BUILD) -o $@ ./$(dir $<)

install-ttrpc-plugin:
	go install github.com/containerd/ttrpc/cmd/protoc-gen-gogottrpc

clean:
	[ -d bin ] && rm -rf bin
