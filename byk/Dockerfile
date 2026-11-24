FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go catalog.txt ./
COPY web/ ./web/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/byk . && mkdir -p /out/data

FROM scratch
COPY --from=build /out/byk /byk
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
ENV DB_PATH=/data/byk.db
EXPOSE 8080
ENTRYPOINT ["/byk"]
