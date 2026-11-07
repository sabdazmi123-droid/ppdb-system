FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ppdb .

FROM alpine:3.21
WORKDIR /app
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/ppdb /app/ppdb
COPY templates /app/templates
COPY static /app/static
ENV APP_ENV=production \
    PPDB_DATA_DIR=/data \
    PPDB_UPLOAD_DIR=/data/uploads
EXPOSE 8085
CMD ["/app/ppdb"]
