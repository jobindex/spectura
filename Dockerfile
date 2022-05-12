from golang:1.18.1

workdir /app

COPY ./go.mod .
COPY ./go.sum .
COPY ./main.go .
COPY ./decap/ ./decap

RUN go build

RUN mv spectura /bin/

CMD ["spectura"]
