#!/usr/bin/env make

GIT_ROOT:=$(shell git rev-parse --show-toplevel)

.PHONY: all clean format lint vet bindata build test docker-deps reap dist

all: clean docker-deps format lint bindata vet build test

clean:
	${GIT_ROOT}/make/clean

format:
	${GIT_ROOT}/make/format

lint:
	${GIT_ROOT}/make/lint

vet:
	${GIT_ROOT}/make/vet

bindata: scripts/configgin/output/configgin.tgz
	${GIT_ROOT}/make/bindata

build:
	${GIT_ROOT}/make/build

dist:
	${GIT_ROOT}/make/package

docker-deps:
	${GIT_ROOT}/make/docker-deps

tools:
	${GIT_ROOT}/make/tools

# If this fails, try running 'make bindata' and rerun 'make test'
test:
	${GIT_ROOT}/make/test

reap:
	${GIT_ROOT}/make/reap

markdown:
	${GIT_ROOT}/make/generate-markdown

configgin: scripts/configgin/output/configgin.tgz

scripts/configgin/output/configgin.tgz:
	${GIT_ROOT}/make/configgin
