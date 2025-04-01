FROM golang:1.22-bullseye as golang

WORKDIR /app

# install dependecies
COPY go.mod go.sum ./
RUN go mod download

# copy sause
COPY *.go image_conf.json ./
COPY xlib/ ./xlib/
COPY decap/ ./decap/
COPY templates/ ./templates/

# build
RUN go build

FROM gcr.io/distroless/base-debian12

WORKDIR /app

COPY --from=golang /app/spectura /bin/spectura
COPY --from=golang /app/image_conf.json image_conf.json
COPY --from=golang /app/templates templates

CMD ["spectura"]
