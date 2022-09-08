from golang:1.19 as golang

workdir /app

# install dependecies
COPY go.mod go.sum ./
RUN go mod download

# copy sause
COPY *.go image_conf.json ./
COPY decap/ ./decap/
COPY templates/ ./templates/

# build
RUN go build

from gcr.io/distroless/base-debian10

workdir /app

COPY --from=golang /app/spectura /bin/spectura
COPY --from=golang /app/image_conf.json image_conf.json
COPY --from=golang /app/templates templates

CMD ["spectura"]
