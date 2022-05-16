from golang:1.18.1

workdir /app

COPY go.mod go.sum cache.go main.go ./
COPY decap/ ./decap/

RUN go build

RUN mv spectura /bin/

CMD ["spectura"]
