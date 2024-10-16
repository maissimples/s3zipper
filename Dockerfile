FROM golang:1.15-alpine AS build

WORKDIR /go/src/s3zipper
COPY . .

RUN apk add git
RUN go get -d -v ./...
RUN go build -v ./...

CMD ["/go/src/s3zipper/s3zipper"]
