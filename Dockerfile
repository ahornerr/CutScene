FROM golang:latest AS build

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /cutscene

FROM alpine:latest

RUN apk update && apk add ffmpeg

WORKDIR /
COPY --from=build /cutscene /cutscene

ENTRYPOINT ["/cutscene"]
