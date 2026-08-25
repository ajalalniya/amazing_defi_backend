FROM golang:1.23-alpine AS build
WORKDIR /src
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -o /amazingd ./cmd/amazingd

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /amazingd /usr/local/bin/amazingd
ENV PROVIDER=mock PORT=8080
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/amazingd"]
