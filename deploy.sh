#!/bin/bash
set -e

# Get DOCKER_PASSWORD and DOCKER_USERNAME from the following url
# https://github.com/maissimples/infrastructure/blob/06cae75f3fccefa74a404060d98b9de4c33f98ae/terraform-kubernetes/production/terraform.tfvars#L67
export DOCKER_PASSWORD=
export DOCKER_USERNAME=

TAG_NAME="s3zipper"
docker build -t $TAG_NAME -f Dockerfile .
docker tag $TAG_NAME ocir.sa-saopaulo-1.oci.oraclecloud.com/gryvnagsfedj/production/$TAG_NAME:latest
echo $DOCKER_PASSWORD | docker login -u $DOCKER_USERNAME --password-stdin ocir.sa-saopaulo-1.oci.oraclecloud.com
docker push ocir.sa-saopaulo-1.oci.oraclecloud.com/gryvnagsfedj/production/$TAG_NAME:latest
kubectl rollout restart -n web deployment/s3zipper