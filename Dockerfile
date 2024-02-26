FROM golang:1.21.7-alpine3.19 AS build

RUN apk update && apk add git && apk add curl

WORKDIR /go/src/github.com/dfroberg/draino
COPY . .
# RUN GOOS=linux GOARCH=amd64 go build -o /draino ./cmd/draino
RUN GOOS=linux go build -o /draino ./cmd/draino

FROM alpine:3.19

RUN apk update && apk add ca-certificates
RUN addgroup -S user && adduser -S user -G user
USER user
COPY --from=build /draino /draino
