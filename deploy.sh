#!/bin/bash
set -e

GCLOUD_PROJECT=$(gcloud config get-value project)
TAG_NAME="s3zipper"
docker build -t $TAG_NAME -f Dockerfile .
docker tag $TAG_NAME us-east1-docker.pkg.dev/$GCLOUD_PROJECT/runrunit/$TAG_NAME:latest
gcloud auth configure-docker us-east1-docker.pkg.dev
docker push us-east1-docker.pkg.dev/$GCLOUD_PROJECT/runrunit/$TAG_NAME:latest
kubectl rollout restart -n web deployment/s3zipper
