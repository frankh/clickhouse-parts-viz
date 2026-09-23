FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /partsviz ./cmd/partsviz

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /partsviz /partsviz
EXPOSE 8080
ENTRYPOINT ["/partsviz"]
