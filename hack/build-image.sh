#!/bin/bash

#docker build -t openshift.io/linuxptp-daemon -f ./Dockerfile .
docker build -t quay.io/aneeshkp/linuxptp-daemon:latest -f ./Dockerfile .
